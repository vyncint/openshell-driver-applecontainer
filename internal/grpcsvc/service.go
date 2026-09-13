// Package grpcsvc implements the openshell.compute.v1.ComputeDriver service
// backed by apple/container VMs.
package grpcsvc

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/vyncint/openshell-driver-applecontainer/internal/backend"
	"github.com/vyncint/openshell-driver-applecontainer/internal/config"
	computev1 "github.com/vyncint/openshell-driver-applecontainer/internal/gen/computev1"
	"github.com/vyncint/openshell-driver-applecontainer/internal/seed"
	"github.com/vyncint/openshell-driver-applecontainer/internal/state"
)

// DriverName is the human-readable identity reported via GetCapabilities.
// Routing uses the gateway-side config key, not this string.
const DriverName = "applecontainer"

// entry is the in-memory registry record for one sandbox.
type entry struct {
	rec      state.Record
	cond     condition
	deleting bool
	// busy marks a StopSandbox/StartSandbox in flight. The poller skips a
	// busy entry so it cannot publish the half-way state (a VM observed
	// stopped a moment before `container start` returns would otherwise be
	// reported as an unexpected exit).
	busy bool
	// cancel aborts an in-flight provisioning task (delete-mid-create).
	cancel context.CancelFunc
	// done is closed when the provisioning task finishes.
	done chan struct{}
}

// Server implements computev1.ComputeDriverServer.
type Server struct {
	computev1.UnimplementedComputeDriverServer

	cfg       config.Config
	rt        backend.Runtime
	store     *state.Store
	log       *slog.Logger
	version   string
	hub       *hub
	extractor *seed.Extractor

	// bgCtx parents provisioning tasks so shutdown cancels them.
	bgCtx    context.Context
	bgCancel context.CancelFunc
	wg       sync.WaitGroup

	mu        sync.Mutex
	sandboxes map[string]*entry
}

// New builds a Server. Version is the build version injected by the linker.
func New(cfg config.Config, rt backend.Runtime, store *state.Store, log *slog.Logger, version string) *Server {
	if log == nil {
		log = slog.Default()
	}
	bgCtx, bgCancel := context.WithCancel(context.Background())
	return &Server{
		cfg:     cfg,
		rt:      rt,
		store:   store,
		log:     log,
		version: version,
		hub:     newHub(log),
		extractor: &seed.Extractor{
			RT:       rt,
			CacheDir: filepath.Join(cfg.StateDir, "cache", "supervisor"),
			Log:      log,
			// So a leaked extraction container is reclaimed as an orphan.
			Labels: map[string]string{labelManagedBy: managedByValue},
		},
		bgCtx:     bgCtx,
		bgCancel:  bgCancel,
		sandboxes: make(map[string]*entry),
	}
}

// Close cancels background provisioning and waits for tasks to finish.
func (s *Server) Close() {
	s.bgCancel()
	s.wg.Wait()
}

func (s *Server) GetCapabilities(_ context.Context, _ *computev1.GetCapabilitiesRequest) (*computev1.GetCapabilitiesResponse, error) {
	return &computev1.GetCapabilitiesResponse{
		DriverName:    DriverName,
		DriverVersion: s.version,
		DefaultImage:  s.cfg.DefaultImage,
		// apple/container VMs belong to the system apiserver and outlive both
		// this driver and the gateway, so the gateway must not stop them at
		// its own shutdown or restart them at startup — that is the docker
		// driver's problem (containers die with the daemon), not ours. A
		// running sandbox stays running across `brew services restart
		// openshell`; Bootstrap re-adopts it.
		GatewayManagesLifecycle: false,
	}, nil
}

// GetGatewayListenerRequirements reports no extra listeners. Guests reach
// the gateway at the vmnet host address, but macOS only materializes that
// address while a VM is attached to the network, so asking the gateway to
// bind it at startup would fail with EADDRNOTAVAIL on an idle host. Setup
// keeps the gateway on 0.0.0.0 with mTLS required instead (see
// hostsetup.GatewayEnvLines).
func (s *Server) GetGatewayListenerRequirements(_ context.Context, _ *computev1.GetGatewayListenerRequirementsRequest) (*computev1.GetGatewayListenerRequirementsResponse, error) {
	return &computev1.GetGatewayListenerRequirementsResponse{}, nil
}

// EnsureWorkspace and DeleteWorkspace are no-ops: a workspace has no
// platform resource here (it is a label on the VM and a field in the
// record), so there is nothing to create or tear down. Answering explicitly
// rather than leaving the RPCs Unimplemented keeps the gateway's logs clean.
func (s *Server) EnsureWorkspace(_ context.Context, _ *computev1.EnsureWorkspaceRequest) (*computev1.EnsureWorkspaceResponse, error) {
	return &computev1.EnsureWorkspaceResponse{}, nil
}

func (s *Server) DeleteWorkspace(_ context.Context, _ *computev1.DeleteWorkspaceRequest) (*computev1.DeleteWorkspaceResponse, error) {
	return &computev1.DeleteWorkspaceResponse{}, nil
}

// validateSandbox holds the checks shared by ValidateSandboxCreate and
// CreateSandbox.
func (s *Server) validateSandbox(sb *computev1.DriverSandbox) error {
	if sb == nil {
		return status.Error(codes.InvalidArgument, "sandbox is required")
	}
	if !state.ValidID(sb.GetId()) {
		return status.Errorf(codes.InvalidArgument, "invalid sandbox id %q", sb.GetId())
	}
	if gpu := sb.GetSpec().GetResourceRequirements().GetGpu(); gpu != nil {
		return status.Error(codes.InvalidArgument, "gpu sandboxes are not supported by the applecontainer driver")
	}
	dcfg, err := parseDriverConfig(sb.GetSpec().GetTemplate().GetDriverConfig())
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	if err := checkDriverConfigPolicy(s.cfg, dcfg); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	// A leading dash would be read as a flag when the ref becomes a
	// positional argument to `container run`/`pull`. Defense in depth: the
	// pull-first flow already rejects such a ref, but fail fast and clearly.
	if img := sb.GetSpec().GetTemplate().GetImage(); strings.HasPrefix(img, "-") {
		return status.Errorf(codes.InvalidArgument, "invalid image reference %q", img)
	}
	// The supervisor rejects an empty argv[0] at boot, which would surface
	// as a sandbox that never becomes Ready; fail the create instead.
	if cmd := sb.GetSpec().GetCommand(); len(cmd) > 0 && cmd[0] == "" {
		return status.Error(codes.InvalidArgument, "sandbox command must not start with an empty argument")
	}
	res := sb.GetSpec().GetTemplate().GetResources()
	for _, q := range []string{res.GetCpuRequest(), res.GetCpuLimit()} {
		if _, err := ParseCPUQuantity(q); err != nil {
			return status.Error(codes.InvalidArgument, err.Error())
		}
	}
	for _, q := range []string{res.GetMemoryRequest(), res.GetMemoryLimit()} {
		if _, err := ParseMemoryQuantityMB(q); err != nil {
			return status.Error(codes.InvalidArgument, err.Error())
		}
	}
	return nil
}

func (s *Server) ValidateSandboxCreate(_ context.Context, req *computev1.ValidateSandboxCreateRequest) (*computev1.ValidateSandboxCreateResponse, error) {
	if err := s.validateSandbox(req.GetSandbox()); err != nil {
		return nil, err
	}
	return &computev1.ValidateSandboxCreateResponse{}, nil
}

func (s *Server) GetSandbox(_ context.Context, req *computev1.GetSandboxRequest) (*computev1.GetSandboxResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.lookupLocked(req.GetSandboxId(), req.GetSandboxName())
	if !ok {
		return nil, status.Errorf(codes.NotFound, "sandbox %q not found", req.GetSandboxId())
	}
	return &computev1.GetSandboxResponse{Sandbox: s.snapshotLocked(e)}, nil
}

func (s *Server) ListSandboxes(_ context.Context, _ *computev1.ListSandboxesRequest) (*computev1.ListSandboxesResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := &computev1.ListSandboxesResponse{}
	for _, e := range s.sandboxes {
		resp.Sandboxes = append(resp.Sandboxes, s.snapshotLocked(e))
	}
	return resp, nil
}

func (s *Server) WatchSandboxes(_ *computev1.WatchSandboxesRequest, stream computev1.ComputeDriver_WatchSandboxesServer) error {
	// Subscribe before snapshotting so no transition between the two is lost;
	// duplicates are harmless (the gateway applies snapshots idempotently).
	id, ch := s.hub.subscribe()
	defer s.hub.unsubscribe(id)

	s.mu.Lock()
	snapshots := make([]*computev1.DriverSandbox, 0, len(s.sandboxes))
	for _, e := range s.sandboxes {
		snapshots = append(snapshots, s.snapshotLocked(e))
	}
	s.mu.Unlock()

	for _, sb := range snapshots {
		ev := &computev1.WatchSandboxesEvent{
			Payload: &computev1.WatchSandboxesEvent_Sandbox{
				Sandbox: &computev1.WatchSandboxesSandboxEvent{Sandbox: sb},
			},
		}
		if err := stream.Send(ev); err != nil {
			return err
		}
	}

	ctx := stream.Context()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(ev); err != nil {
				return err
			}
		}
	}
}

// lookupLocked resolves an entry by id, falling back to the sandbox name.
func (s *Server) lookupLocked(id, name string) (*entry, bool) {
	if e, ok := s.sandboxes[id]; ok {
		return e, true
	}
	if name == "" {
		return nil, false
	}
	for _, e := range s.sandboxes {
		if e.rec.Name == name {
			return e, true
		}
	}
	return nil, false
}

// snapshotLocked renders the current driver-native observation for e.
func (s *Server) snapshotLocked(e *entry) *computev1.DriverSandbox {
	return &computev1.DriverSandbox{
		Id:        e.rec.ID,
		Name:      e.rec.Name,
		Namespace: s.cfg.Namespace,
		Workspace: e.rec.Workspace,
		Status: &computev1.DriverSandboxStatus{
			SandboxName: e.rec.ContainerName,
			InstanceId:  e.rec.ContainerName,
			Conditions:  []*computev1.DriverCondition{e.cond.proto()},
			Deleting:    e.deleting,
		},
	}
}

// publishSandbox emits a watch event with the sandbox's current snapshot.
func (s *Server) publishSandbox(e *entry) {
	s.mu.Lock()
	sb := s.snapshotLocked(e)
	s.mu.Unlock()
	s.hub.publish(&computev1.WatchSandboxesEvent{
		Payload: &computev1.WatchSandboxesEvent_Sandbox{
			Sandbox: &computev1.WatchSandboxesSandboxEvent{Sandbox: sb},
		},
	})
}

// publishDeleted emits a deletion watch event.
func (s *Server) publishDeleted(id string) {
	s.hub.publish(&computev1.WatchSandboxesEvent{
		Payload: &computev1.WatchSandboxesEvent_Deleted{
			Deleted: &computev1.WatchSandboxesDeletedEvent{SandboxId: id},
		},
	})
}
