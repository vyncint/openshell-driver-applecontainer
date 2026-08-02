package grpcsvc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/vyncint/openshell-driver-applecontainer/internal/backend"
	computev1 "github.com/vyncint/openshell-driver-applecontainer/internal/gen/computev1"
	"github.com/vyncint/openshell-driver-applecontainer/internal/seed"
	"github.com/vyncint/openshell-driver-applecontainer/internal/state"
)

// containerNamePrefix namespaces every VM this driver manages.
const containerNamePrefix = "oshl-"

// deleteTimeout bounds a delete's detached teardown so a wedged
// provisioning task cannot block it indefinitely.
const deleteTimeout = 30 * time.Second

// Labels applied to every managed container; reconcile identifies ours by
// the managed-by marker.
const (
	labelManagedBy = "openshell.ai/managed-by"
	managedByValue = "openshell-driver-applecontainer"
	labelSandboxID = "openshell.ai/sandbox-id"
	labelName      = "openshell.ai/sandbox-name"
	labelNamespace = "openshell.ai/sandbox-namespace"
	labelWorkspace = "openshell.ai/sandbox-workspace"
)

func containerName(id string) string { return containerNamePrefix + id }

// CreateSandbox accepts quickly and provisions in the background, in the
// upstream VM driver's accept-then-provision shape: the record is persisted
// before any boot attempt, the response is empty, and progress flows through
// the watch stream.
func (s *Server) CreateSandbox(_ context.Context, req *computev1.CreateSandboxRequest) (*computev1.CreateSandboxResponse, error) {
	sb := req.GetSandbox()
	s.log.Info("create sandbox requested",
		"sandbox_id", sb.GetId(),
		"name", sb.GetName(),
		"workspace", sb.GetWorkspace(),
		"image", sb.GetSpec().GetTemplate().GetImage(),
	)
	if err := s.validateSandbox(sb); err != nil {
		return nil, err
	}
	if s.cfg.GRPCEndpoint == "" {
		return nil, status.Error(codes.FailedPrecondition,
			"driver has no --grpc-endpoint configured; set it to the gateway address reachable from guest VMs")
	}

	imageRef := sb.GetSpec().GetTemplate().GetImage()
	if imageRef == "" {
		imageRef = s.cfg.DefaultImage
	}

	// Persist the accepted request with the secret token redacted: VMs
	// survive driver restarts, so the token is never re-injected from disk.
	redacted := proto.Clone(sb).(*computev1.DriverSandbox)
	if redacted.GetSpec() != nil {
		redacted.Spec.SandboxToken = ""
	}
	sandboxJSON, err := protojson.Marshal(redacted)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encode sandbox record: %v", err)
	}

	rec := state.Record{
		ID:            sb.GetId(),
		Name:          sb.GetName(),
		Namespace:     s.cfg.Namespace,
		Workspace:     sb.GetWorkspace(),
		ContainerName: containerName(sb.GetId()),
		ImageRef:      imageRef,
		CreatedAt:     time.Now().UTC(),
		Sandbox:       sandboxJSON,
	}

	s.mu.Lock()
	if _, exists := s.sandboxes[rec.ID]; exists {
		s.mu.Unlock()
		return nil, status.Errorf(codes.AlreadyExists, "sandbox %q already exists", rec.ID)
	}
	if err := s.store.Save(rec); err != nil {
		s.mu.Unlock()
		return nil, status.Errorf(codes.Internal, "persist sandbox record: %v", err)
	}
	ctx, cancel := context.WithCancel(s.bgCtx)
	e := &entry{rec: rec, cond: startingCondition(), cancel: cancel, done: make(chan struct{})}
	s.sandboxes[rec.ID] = e
	s.mu.Unlock()

	s.publishSandbox(e)
	s.publishPlatformEvent(rec.ID, "Normal", "Scheduled", "Sandbox accepted by the applecontainer driver")

	token := sb.GetSpec().GetSandboxToken()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(e.done)
		s.provision(ctx, e, sb, token)
	}()

	return &computev1.CreateSandboxResponse{}, nil
}

// provision runs the boot pipeline and converts the outcome into conditions
// and events. A canceled context means a delete raced the create; the
// delete path owns cleanup then.
func (s *Server) provision(ctx context.Context, e *entry, sb *computev1.DriverSandbox, token string) {
	err := s.provisionInner(ctx, e, sb, token)
	if err == nil {
		s.setCondition(e, readyTrueCondition())
		s.publishSandbox(e)
		s.publishPlatformEvent(e.rec.ID, "Normal", "Started", "Sandbox VM started")
		return
	}
	if errors.Is(err, context.Canceled) || ctx.Err() != nil {
		s.log.Info("provisioning canceled", "sandbox_id", e.rec.ID)
		return
	}
	s.log.Error("provisioning failed", "sandbox_id", e.rec.ID, "err", err)
	s.setCondition(e, failedCondition(err.Error()))
	s.publishSandbox(e)
	s.publishPlatformEvent(e.rec.ID, "Warning", "ProvisioningFailed", err.Error())
}

func (s *Server) provisionInner(ctx context.Context, e *entry, sb *computev1.DriverSandbox, token string) error {
	rec := e.rec

	dcfg, err := parseDriverConfig(sb.GetSpec().GetTemplate().GetDriverConfig())
	if err != nil {
		return err
	}

	// Image: local first, pull only when absent.
	findImage := func() (backend.Image, bool, error) {
		images, err := s.rt.ImageList(ctx)
		if err != nil {
			return backend.Image{}, false, fmt.Errorf("list images: %w", err)
		}
		for _, img := range images {
			if img.Reference == rec.ImageRef {
				return img, true, nil
			}
		}
		return backend.Image{}, false, nil
	}
	img, present, err := findImage()
	if err != nil {
		return err
	}
	if !present {
		s.publishPlatformEvent(rec.ID, "Normal", "Pulling", "Pulling image "+rec.ImageRef)
		if err := s.rt.ImagePull(ctx, rec.ImageRef, "linux/arm64"); err != nil {
			return fmt.Errorf("pull image %s: %w", rec.ImageRef, err)
		}
		s.publishPlatformEvent(rec.ID, "Normal", "Pulled", "Pulled image "+rec.ImageRef)
		if img, present, err = findImage(); err != nil {
			return err
		} else if !present {
			return fmt.Errorf("image %s not present after pull", rec.ImageRef)
		}
	}

	// Pin the digest observed at inspect time and boot strictly by it, so
	// a tag moving between now and the run cannot swap the image (podman
	// driver parity). The pinned identity is persisted for reconcile.
	runImage := rec.ImageRef
	if img.Digest != "" {
		runImage = refWithDigest(rec.ImageRef, img.Digest)
		s.mu.Lock()
		e.rec.ImageDigest = img.Digest
		rec = e.rec
		s.mu.Unlock()
		if err := s.store.Save(rec); err != nil {
			return fmt.Errorf("persist pinned digest: %w", err)
		}
	}

	// Supervisor binary (cached per supervisor-image digest).
	supPath, err := s.extractor.Ensure(ctx, s.cfg.SupervisorImage)
	if err != nil {
		return err
	}

	// Per-sandbox seed directory.
	seedDir := filepath.Join(s.store.SandboxDir(rec.ID), "seed")
	if err := seed.Write(seedDir, seed.Materials{
		SupervisorPath: supPath,
		CAPath:         s.cfg.GuestTLSCA,
		CertPath:       s.cfg.GuestTLSCert,
		KeyPath:        s.cfg.GuestTLSKey,
		Token:          token,
	}); err != nil {
		return err
	}

	env, err := sandboxEnv(s.cfg, sb, token != "")
	if err != nil {
		return fmt.Errorf("build environment: %w", err)
	}
	// The supervisor drops the workload to the image's intended user; it
	// learns that identity from this variable (docker-driver parity).
	env[envOCIImageUser] = img.User

	labels := make(map[string]string)
	for k, v := range sb.GetSpec().GetTemplate().GetLabels() {
		labels[k] = v
	}
	labels[labelManagedBy] = managedByValue
	labels[labelSandboxID] = rec.ID
	labels[labelName] = rec.Name
	labels[labelNamespace] = rec.Namespace
	labels[labelWorkspace] = rec.Workspace

	// Sizing: request-supplied resources win; the driver config is the
	// fallback. Limits take precedence over requests.
	res := sb.GetSpec().GetTemplate().GetResources()
	cpus, err := firstQuantity(ParseCPUQuantity, res.GetCpuLimit(), res.GetCpuRequest())
	if err != nil {
		return err
	}
	if cpus == 0 {
		cpus = s.cfg.CPUs
	}
	memMB, err := firstQuantity(ParseMemoryQuantityMB, res.GetMemoryLimit(), res.GetMemoryRequest())
	if err != nil {
		return err
	}
	if memMB == 0 {
		memMB = s.cfg.MemoryMB
	}

	network := s.cfg.Network
	if dcfg.Network != "" {
		network = dcfg.Network
	}
	// Kernel: per-sandbox driver config wins over the driver-level default
	// (the fleet-wide Landlock escape hatch).
	kernel := s.cfg.Kernel
	if dcfg.Kernel != "" {
		kernel = dcfg.Kernel
	}
	if kernel != "" {
		if _, err := os.Stat(kernel); err != nil {
			// The path may come from the driver default or the per-sandbox
			// config; name it explicitly so the failure is diagnosable.
			return fmt.Errorf("kernel %q is not usable: %w", kernel, err)
		}
	}

	volumes := []backend.VolumeMount{{
		HostPath:  seedDir,
		GuestPath: seed.GuestSeedDir,
		ReadOnly:  true,
	}}
	var tmpfs []string
	for _, m := range dcfg.Mounts {
		switch m.Type {
		case "volume":
			volumes = append(volumes, backend.VolumeMount{
				HostPath:  m.Source,
				GuestPath: m.Target,
				ReadOnly:  m.readOnly(),
			})
		case "tmpfs":
			tmpfs = append(tmpfs, m.Target)
		}
	}

	// container run is not idempotent for an existing name: always clear
	// any leftover VM with our name first.
	if err := s.rt.Delete(ctx, rec.ContainerName); err != nil && !errors.Is(err, backend.ErrNotFound) {
		return fmt.Errorf("clear stale container: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	// The boot shim and supervisor need guest root regardless of the
	// image's USER; the workload later drops to the image user. The caps
	// mirror the upstream docker driver: the guest init applies default
	// OCI capabilities even for uid 0, and the supervisor's netns setup
	// needs SYS_ADMIN and NET_ADMIN on top of them.
	root := int64(0)
	if _, err := s.rt.Run(ctx, backend.RunSpec{
		Name:       rec.ContainerName,
		Image:      runImage,
		Network:    network,
		Volumes:    volumes,
		Tmpfs:      tmpfs,
		Env:        env,
		Labels:     labels,
		CPUs:       cpus,
		MemoryMB:   memMB,
		Entrypoint: seed.GuestSeedDir + "/boot.sh",
		Kernel:     kernel,
		UID:        &root,
		GID:        &root,
		CapAdd:     []string{"CAP_SYS_ADMIN", "CAP_NET_ADMIN", "CAP_SYS_PTRACE", "CAP_SYSLOG"},
	}); err != nil {
		return fmt.Errorf("boot sandbox VM: %w", err)
	}
	return nil
}

// firstQuantity parses candidates in order and returns the first non-zero
// value.
func firstQuantity(parse func(string) (int64, error), candidates ...string) (int64, error) {
	for _, c := range candidates {
		v, err := parse(c)
		if err != nil {
			return 0, err
		}
		if v > 0 {
			return v, nil
		}
	}
	return 0, nil
}

// refWithDigest pins an image reference to a digest, dropping any tag
// (name:tag@digest and name@digest resolve identically; the digest rules).
func refWithDigest(ref, digest string) string {
	if strings.Contains(ref, "@") {
		return ref
	}
	name := ref
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		name = ref[:i]
	}
	return name + "@" + digest
}

// DeleteSandbox tears the VM down. A delete racing an in-flight create
// cancels provisioning first, then removes whatever was already built.
func (s *Server) DeleteSandbox(ctx context.Context, req *computev1.DeleteSandboxRequest) (*computev1.DeleteSandboxResponse, error) {
	s.log.Info("delete sandbox requested", "sandbox_id", req.GetSandboxId(), "name", req.GetSandboxName())

	// Teardown, once begun, runs under a detached context: a canceled or
	// short-deadline delete RPC must not leave the sandbox half-removed —
	// that leftover is skipped by the poller and re-adopted on restart, so
	// the delete would be silently lost.
	teardownCtx, tcancel := context.WithTimeout(context.WithoutCancel(ctx), deleteTimeout)
	defer tcancel()

	s.mu.Lock()
	e, ok := s.lookupLocked(req.GetSandboxId(), req.GetSandboxName())
	if !ok {
		s.mu.Unlock()
		// Nothing known: still try to clear a container by conventional
		// name so an orphaned VM cannot survive a lost record.
		deleted := false
		if state.ValidID(req.GetSandboxId()) {
			if err := s.rt.Delete(teardownCtx, containerName(req.GetSandboxId())); err == nil {
				deleted = true
			} else if !errors.Is(err, backend.ErrNotFound) {
				return nil, status.Errorf(codes.Internal, "delete container: %v", err)
			}
			_ = s.store.Delete(req.GetSandboxId())
		}
		return &computev1.DeleteSandboxResponse{Deleted: deleted}, nil
	}
	id := e.rec.ID
	name := e.rec.ContainerName
	e.deleting = true
	e.cond = deletingCondition()
	cancel := e.cancel
	done := e.done
	s.mu.Unlock()

	s.publishSandbox(e)
	if cancel != nil {
		cancel()
	}
	// Wait for provisioning to stop, bounded by the detached timeout rather
	// than the request context, then tear down regardless.
	if done != nil {
		select {
		case <-done:
		case <-teardownCtx.Done():
		}
	}

	deleted := true
	if err := s.rt.Delete(teardownCtx, name); err != nil {
		if errors.Is(err, backend.ErrNotFound) {
			deleted = false
		} else {
			return nil, status.Errorf(codes.Internal, "delete container: %v", err)
		}
	}
	if err := s.store.Delete(id); err != nil {
		return nil, status.Errorf(codes.Internal, "delete record: %v", err)
	}

	s.mu.Lock()
	delete(s.sandboxes, id)
	s.mu.Unlock()
	s.publishDeleted(id)

	return &computev1.DeleteSandboxResponse{Deleted: deleted}, nil
}

// publishPlatformEvent emits a raw platform event correlated to a sandbox.
func (s *Server) publishPlatformEvent(id, typ, reason, message string) {
	s.hub.publish(&computev1.WatchSandboxesEvent{
		Payload: &computev1.WatchSandboxesEvent_PlatformEvent{
			PlatformEvent: &computev1.WatchSandboxesPlatformEvent{
				SandboxId: id,
				Event: &computev1.DriverPlatformEvent{
					TimestampMs: time.Now().UnixMilli(),
					Source:      DriverName,
					Type:        typ,
					Reason:      reason,
					Message:     message,
				},
			},
		},
	})
}

// setCondition updates an entry's condition under the registry lock.
func (s *Server) setCondition(e *entry, c condition) {
	s.mu.Lock()
	e.cond = c
	s.mu.Unlock()
}
