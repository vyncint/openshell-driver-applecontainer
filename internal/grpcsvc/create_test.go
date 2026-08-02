package grpcsvc

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/vyncint/openshell-driver-applecontainer/internal/backend"
	"github.com/vyncint/openshell-driver-applecontainer/internal/config"
	computev1 "github.com/vyncint/openshell-driver-applecontainer/internal/gen/computev1"
	"github.com/vyncint/openshell-driver-applecontainer/internal/state"
)

const (
	testSandboxID = "0195c1a2-1111-2222-3333-444444444555"
	testImage     = "ghcr.io/nvidia/openshell-community/sandboxes/base:latest"
	testSupImage  = "ghcr.io/nvidia/openshell/supervisor:0.0.96"
)

// liveTestConfig returns a config with everything provisioning needs,
// pointing TLS paths at real temp files.
func liveTestConfig(t *testing.T) config.Config {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"ca.crt":  "test-ca",
		"tls.crt": "test-cert",
		"tls.key": "test-key",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := testConfig()
	cfg.StateDir = t.TempDir()
	cfg.GRPCEndpoint = "https://192.168.65.1:17670"
	cfg.SupervisorImage = testSupImage
	cfg.GuestTLSCA = filepath.Join(dir, "ca.crt")
	cfg.GuestTLSCert = filepath.Join(dir, "tls.crt")
	cfg.GuestTLSKey = filepath.Join(dir, "tls.key")
	return cfg
}

func newLiveServer(t *testing.T, fake *backend.Fake) *Server {
	t.Helper()
	cfg := liveTestConfig(t)
	store, err := state.NewStore(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	fake.AddImageWithUser(testImage, "sha256:baseimg", "sandbox")
	fake.AddImage(testSupImage, "sha256:supimg")
	srv := New(cfg, fake, store, slog.Default(), "test")
	t.Cleanup(srv.Close)
	return srv
}

func createRequest() *computev1.CreateSandboxRequest {
	id := testSandboxID
	return &computev1.CreateSandboxRequest{
		Sandbox: &computev1.DriverSandbox{
			Id:        id,
			Name:      "sb-" + id[:8],
			Workspace: "default",
			Spec: &computev1.DriverSandboxSpec{
				LogLevel:     "debug",
				Environment:  map[string]string{"USER_VAR": "from-spec"},
				SandboxToken: "jwt-token-value",
				Template: &computev1.DriverSandboxTemplate{
					Image:       testImage,
					Environment: map[string]string{"USER_VAR": "from-template", "TPL_ONLY": "1"},
					Labels:      map[string]string{"user/label": "x"},
				},
			},
		},
	}
}

// waitForCondition polls until the test sandbox's condition reason matches.
func waitForCondition(t *testing.T, srv *Server, reason string) condition {
	t.Helper()
	id := testSandboxID
	deadline := time.Now().Add(5 * time.Second)
	for {
		srv.mu.Lock()
		e, ok := srv.sandboxes[id]
		var cond condition
		if ok {
			cond = e.cond
		}
		srv.mu.Unlock()
		if ok && cond.Reason == reason {
			return cond
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition %q never reached; last: %+v", reason, cond)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCreateSandboxProvisionsVM(t *testing.T) {
	fake := &backend.Fake{}
	srv := newLiveServer(t, fake)
	client := dialTestServer(t, srv)

	if _, err := client.CreateSandbox(context.Background(), createRequest()); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, srv, reasonBackendReady)

	// The sandbox VM run call is the last one (extraction ran first).
	calls := fake.RunCalls()
	var boot *backend.RunSpec
	for i := range calls {
		if calls[i].Name == "oshl-"+testSandboxID {
			boot = &calls[i]
		}
	}
	if boot == nil {
		t.Fatalf("no boot run call found in %+v", calls)
	}
	wantPinned := "ghcr.io/nvidia/openshell-community/sandboxes/base@sha256:baseimg"
	if boot.Image != wantPinned || boot.Network != "oshl" {
		t.Errorf("boot image/network = %q/%q, want %q/oshl", boot.Image, boot.Network, wantPinned)
	}
	if boot.Entrypoint != "/openshell-seed/boot.sh" {
		t.Errorf("entrypoint = %q", boot.Entrypoint)
	}
	if len(boot.Volumes) != 1 || boot.Volumes[0].GuestPath != "/openshell-seed" || !boot.Volumes[0].ReadOnly {
		t.Errorf("volumes = %+v", boot.Volumes)
	}
	if boot.CPUs != 2 || boot.MemoryMB != 2048 {
		t.Errorf("resources = %d cpu / %d MB", boot.CPUs, boot.MemoryMB)
	}

	env := boot.Env
	wantEnv := map[string]string{
		"OPENSHELL_ENDPOINT":           "https://192.168.65.1:17670",
		"OPENSHELL_SANDBOX_ID":         testSandboxID,
		"OPENSHELL_SANDBOX":            "sb-" + testSandboxID[:8],
		"OPENSHELL_SSH_SOCKET_PATH":    "/run/openshell/ssh.sock",
		"OPENSHELL_SANDBOX_COMMAND":    "sleep infinity",
		"OPENSHELL_LOG_LEVEL":          "debug",
		"OPENSHELL_TLS_CA":             "/openshell-seed/tls/ca.crt",
		"OPENSHELL_TLS_CERT":           "/openshell-seed/tls/tls.crt",
		"OPENSHELL_TLS_KEY":            "/openshell-seed/tls/tls.key",
		"OPENSHELL_SANDBOX_TOKEN_FILE": "/openshell-seed/auth/sandbox.jwt",
		"USER_VAR":                     "from-spec", // spec wins over template
		"TPL_ONLY":                     "1",
		"HOME":                         "/root",
		"TERM":                         "xterm",
	}
	for k, v := range wantEnv {
		if env[k] != v {
			t.Errorf("env[%s] = %q, want %q", k, env[k], v)
		}
	}
	if _, ok := env["OPENSHELL_SANDBOX_TOKEN"]; ok {
		t.Error("raw token must never be in env")
	}
	if env["OPENSHELL_OCI_IMAGE_USER"] != "sandbox" {
		t.Errorf("oci image user = %q, want image's USER", env["OPENSHELL_OCI_IMAGE_USER"])
	}
	if boot.UID == nil || *boot.UID != 0 || boot.GID == nil || *boot.GID != 0 {
		t.Errorf("boot must run as guest root, got uid=%v gid=%v", boot.UID, boot.GID)
	}
	if !strings.Contains(env["OPENSHELL_USER_ENVIRONMENT"], "from-spec") {
		t.Errorf("user environment json = %q", env["OPENSHELL_USER_ENVIRONMENT"])
	}

	if boot.Labels[labelManagedBy] != managedByValue || boot.Labels[labelSandboxID] != testSandboxID {
		t.Errorf("labels = %+v", boot.Labels)
	}
	if boot.Labels["user/label"] != "x" {
		t.Errorf("user labels not carried: %+v", boot.Labels)
	}

	// Seed dir contents on disk.
	seedDir := filepath.Join(srv.store.SandboxDir(testSandboxID), "seed")
	for f, wantPerm := range map[string]os.FileMode{
		"openshell-sandbox": 0o600, // boot.sh re-copies and sets +x in the guest
		"boot.sh":           0o700, // entrypoint: executable
		"tls/ca.crt":        0o600,
		"tls/tls.key":       0o600,
		"auth/sandbox.jwt":  0o600,
	} {
		info, err := os.Stat(filepath.Join(seedDir, f))
		if err != nil {
			t.Errorf("seed file %s: %v", f, err)
			continue
		}
		if info.Mode().Perm() != wantPerm {
			t.Errorf("seed %s perms = %o, want %o", f, info.Mode().Perm(), wantPerm)
		}
	}
	token, err := os.ReadFile(filepath.Join(seedDir, "auth", "sandbox.jwt"))
	if err != nil || string(token) != "jwt-token-value\n" {
		t.Errorf("token file = %q, %v", token, err)
	}

	// Persisted record must not contain the token.
	rec, err := srv.store.Load(testSandboxID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rec.Sandbox), "jwt-token-value") {
		t.Error("persisted record leaks the sandbox token")
	}

	// List now reports the sandbox with Ready=True.
	resp, err := client.ListSandboxes(context.Background(), &computev1.ListSandboxesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetSandboxes()) != 1 {
		t.Fatalf("list = %+v", resp.GetSandboxes())
	}
	got := resp.GetSandboxes()[0]
	if got.GetStatus().GetConditions()[0].GetStatus() != "True" {
		t.Errorf("condition = %+v", got.GetStatus().GetConditions()[0])
	}
}

func TestCreateSandboxAppliesDriverConfigAndResources(t *testing.T) {
	fake := &backend.Fake{}
	srv := newLiveServer(t, fake)
	client := dialTestServer(t, srv)

	kernelPath := filepath.Join(t.TempDir(), "vmlinux")
	if err := os.WriteFile(kernelPath, []byte("kernel"), 0o644); err != nil {
		t.Fatal(err)
	}

	req := createRequest()
	req.Sandbox.Spec.Template.Resources = &computev1.DriverResourceRequirements{
		CpuLimit:      "3",
		MemoryRequest: "4Gi",
	}
	req.Sandbox.Spec.Template.DriverConfig = mustStruct(t, map[string]any{
		"network": "othernet",
		"kernel":  kernelPath,
		"mounts": []any{
			map[string]any{"type": "volume", "source": "/Users/x/data", "target": "/data"},
			map[string]any{"type": "tmpfs", "target": "/scratch"},
		},
	})

	if _, err := client.CreateSandbox(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, srv, reasonBackendReady)

	calls := fake.RunCalls()
	boot := calls[len(calls)-1]
	if boot.Name != "oshl-"+testSandboxID {
		t.Fatalf("last run call = %+v", boot)
	}
	if boot.CPUs != 3 || boot.MemoryMB != 4096 {
		t.Errorf("resources = %d cpu / %d MB, want 3/4096", boot.CPUs, boot.MemoryMB)
	}
	if boot.Network != "othernet" {
		t.Errorf("network = %q", boot.Network)
	}
	if boot.Kernel != kernelPath {
		t.Errorf("kernel = %q", boot.Kernel)
	}
	if !strings.HasPrefix(boot.Image, "ghcr.io/nvidia/openshell-community/sandboxes/base@sha256:") {
		t.Errorf("image not digest-pinned: %q", boot.Image)
	}
	if len(boot.Volumes) != 2 || boot.Volumes[1].GuestPath != "/data" || !boot.Volumes[1].ReadOnly {
		t.Errorf("volumes = %+v", boot.Volumes)
	}
	if len(boot.Tmpfs) != 1 || boot.Tmpfs[0] != "/scratch" {
		t.Errorf("tmpfs = %+v", boot.Tmpfs)
	}

	// The pinned digest is persisted for reconcile.
	rec, err := srv.store.Load(testSandboxID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.ImageDigest != "sha256:baseimg" {
		t.Errorf("persisted digest = %q", rec.ImageDigest)
	}
}

func TestCreateSandboxUsesDriverDefaultKernel(t *testing.T) {
	fake := &backend.Fake{}
	srv := newLiveServer(t, fake)
	kernelPath := filepath.Join(t.TempDir(), "vmlinux-landlock")
	if err := os.WriteFile(kernelPath, []byte("kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv.cfg.Kernel = kernelPath
	client := dialTestServer(t, srv)

	if _, err := client.CreateSandbox(context.Background(), createRequest()); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, srv, reasonBackendReady)

	calls := fake.RunCalls()
	boot := calls[len(calls)-1]
	if boot.Kernel != kernelPath {
		t.Errorf("boot kernel = %q, want driver default %q", boot.Kernel, kernelPath)
	}
}

func TestDriverConfigKernelOverridesDefault(t *testing.T) {
	fake := &backend.Fake{}
	srv := newLiveServer(t, fake)
	dir := t.TempDir()
	defaultKernel := filepath.Join(dir, "vmlinux-default")
	perSandbox := filepath.Join(dir, "vmlinux-override")
	for _, p := range []string{defaultKernel, perSandbox} {
		if err := os.WriteFile(p, []byte("kernel"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srv.cfg.Kernel = defaultKernel
	client := dialTestServer(t, srv)

	req := createRequest()
	req.Sandbox.Spec.Template.DriverConfig = mustStruct(t, map[string]any{"kernel": perSandbox})
	if _, err := client.CreateSandbox(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, srv, reasonBackendReady)

	calls := fake.RunCalls()
	boot := calls[len(calls)-1]
	if boot.Kernel != perSandbox {
		t.Errorf("boot kernel = %q, want per-sandbox override %q", boot.Kernel, perSandbox)
	}
}

func TestMissingDefaultKernelFailsProvisioning(t *testing.T) {
	fake := &backend.Fake{}
	srv := newLiveServer(t, fake)
	srv.cfg.Kernel = "/nonexistent/vmlinux"
	client := dialTestServer(t, srv)

	if _, err := client.CreateSandbox(context.Background(), createRequest()); err != nil {
		t.Fatal(err)
	}
	cond := waitForCondition(t, srv, reasonProvisioningFailed)
	if !strings.Contains(cond.Message, "kernel") || !strings.Contains(cond.Message, "/nonexistent/vmlinux") {
		t.Errorf("failure message must name the kernel path, got %q", cond.Message)
	}
}

func TestValidateRejectsBadDriverConfigAndResources(t *testing.T) {
	client := dialTestServer(t, newTestServer(t))
	ctx := context.Background()

	badMount := createRequest()
	badMount.Sandbox.Spec.Template.DriverConfig = mustStruct(t, map[string]any{
		"mounts": []any{map[string]any{"type": "volume", "source": "/tmp/x", "target": "/sandbox"}},
	})
	if _, err := client.ValidateSandboxCreate(ctx, &computev1.ValidateSandboxCreateRequest{Sandbox: badMount.Sandbox}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("reserved mount target: want InvalidArgument, got %v", err)
	}

	badRes := createRequest()
	badRes.Sandbox.Spec.Template.Resources = &computev1.DriverResourceRequirements{CpuLimit: "many"}
	if _, err := client.ValidateSandboxCreate(ctx, &computev1.ValidateSandboxCreateRequest{Sandbox: badRes.Sandbox}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("bad cpu quantity: want InvalidArgument, got %v", err)
	}

	badImage := createRequest()
	badImage.Sandbox.Spec.Template.Image = "--privileged"
	if _, err := client.ValidateSandboxCreate(ctx, &computev1.ValidateSandboxCreateRequest{Sandbox: badImage.Sandbox}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("flag-like image ref: want InvalidArgument, got %v", err)
	}
}

func TestRefWithDigest(t *testing.T) {
	tests := []struct{ ref, digest, want string }{
		{"ghcr.io/a/b:latest", "sha256:x", "ghcr.io/a/b@sha256:x"},
		{"ghcr.io/a/b", "sha256:x", "ghcr.io/a/b@sha256:x"},
		{"registry:5000/a/b:1.0", "sha256:x", "registry:5000/a/b@sha256:x"},
		{"ghcr.io/a/b@sha256:y", "sha256:x", "ghcr.io/a/b@sha256:y"},
	}
	for _, tt := range tests {
		if got := refWithDigest(tt.ref, tt.digest); got != tt.want {
			t.Errorf("refWithDigest(%q) = %q, want %q", tt.ref, got, tt.want)
		}
	}
}

func TestCreateSandboxDuplicate(t *testing.T) {
	fake := &backend.Fake{}
	srv := newLiveServer(t, fake)
	client := dialTestServer(t, srv)

	if _, err := client.CreateSandbox(context.Background(), createRequest()); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, srv, reasonBackendReady)

	_, err := client.CreateSandbox(context.Background(), createRequest())
	if status.Code(err) != codes.AlreadyExists {
		t.Errorf("want AlreadyExists, got %v", err)
	}
}

func TestCreateSandboxProvisionFailure(t *testing.T) {
	fake := &backend.Fake{}
	srv := newLiveServer(t, fake)
	client := dialTestServer(t, srv)

	// Pre-extract the supervisor so the failure hits the sandbox boot.
	if _, err := srv.extractor.Ensure(context.Background(), testSupImage); err != nil {
		t.Fatal(err)
	}
	fake.RunError = context.DeadlineExceeded // any error will do

	if _, err := client.CreateSandbox(context.Background(), createRequest()); err != nil {
		t.Fatal(err)
	}
	cond := waitForCondition(t, srv, reasonProvisioningFailed)
	if cond.Status != "False" {
		t.Errorf("failed condition = %+v", cond)
	}
	// Record survives for reconcile/inspection.
	if _, err := srv.store.Load(testSandboxID); err != nil {
		t.Errorf("record gone after failure: %v", err)
	}
}

func TestDeleteSandbox(t *testing.T) {
	fake := &backend.Fake{}
	srv := newLiveServer(t, fake)
	client := dialTestServer(t, srv)

	if _, err := client.CreateSandbox(context.Background(), createRequest()); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, srv, reasonBackendReady)

	resp, err := client.DeleteSandbox(context.Background(), &computev1.DeleteSandboxRequest{SandboxId: testSandboxID})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.GetDeleted() {
		t.Error("want deleted=true")
	}
	if _, err := fake.Get(context.Background(), "oshl-"+testSandboxID); err == nil {
		t.Error("container still exists after delete")
	}
	if _, err := srv.store.Load(testSandboxID); err == nil {
		t.Error("record still exists after delete")
	}
	if _, err := client.GetSandbox(context.Background(), &computev1.GetSandboxRequest{SandboxId: testSandboxID}); status.Code(err) != codes.NotFound {
		t.Errorf("want NotFound after delete, got %v", err)
	}
}

func TestDeleteCompletesUnderCanceledContext(t *testing.T) {
	fake := &backend.Fake{}
	srv := newLiveServer(t, fake)
	client := dialTestServer(t, srv)

	if _, err := client.CreateSandbox(context.Background(), createRequest()); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, srv, reasonBackendReady)

	// A canceled request context must not abort teardown: once committed to
	// deleting, the VM and record must still be removed (otherwise a
	// restart would re-adopt the "deleted" sandbox). Call the handler
	// directly so the context is genuinely canceled at entry.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := srv.DeleteSandbox(ctx, &computev1.DeleteSandboxRequest{SandboxId: testSandboxID}); err != nil {
		t.Fatalf("delete under canceled ctx returned error: %v", err)
	}
	if _, err := fake.Get(context.Background(), "oshl-"+testSandboxID); err == nil {
		t.Error("VM survived a canceled delete")
	}
	if _, err := srv.store.Load(testSandboxID); err == nil {
		t.Error("record survived a canceled delete")
	}
	srv.mu.Lock()
	_, stuck := srv.sandboxes[testSandboxID]
	srv.mu.Unlock()
	if stuck {
		t.Error("registry entry left in deleting state after a canceled delete")
	}
}

func TestDeleteUnknownSandbox(t *testing.T) {
	fake := &backend.Fake{}
	srv := newLiveServer(t, fake)
	client := dialTestServer(t, srv)

	resp, err := client.DeleteSandbox(context.Background(), &computev1.DeleteSandboxRequest{SandboxId: "0195c1a2-9999-9999-9999-999999999999"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetDeleted() {
		t.Error("want deleted=false for unknown sandbox")
	}
}

func TestDeleteMidCreateCancelsProvisioning(t *testing.T) {
	fake := &backend.Fake{RunBlock: make(chan struct{})}
	srv := newLiveServer(t, fake)
	client := dialTestServer(t, srv)

	if _, err := client.CreateSandbox(context.Background(), createRequest()); err != nil {
		t.Fatal(err)
	}
	// Provisioning is now blocked inside the extraction container's Run.
	// Delete must cancel it and clean up without ever booting the VM.
	done := make(chan error, 1)
	go func() {
		resp, err := client.DeleteSandbox(context.Background(), &computev1.DeleteSandboxRequest{SandboxId: testSandboxID})
		if err == nil && resp.GetDeleted() {
			// Deleted=true is acceptable only if a container existed;
			// either way the invariants below are what matter.
			_ = resp
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("delete failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delete did not complete while create was in flight")
	}

	// No sandbox VM must exist, no record, no registry entry.
	if _, err := fake.Get(context.Background(), "oshl-"+testSandboxID); err == nil {
		t.Error("sandbox VM exists despite canceled create")
	}
	if _, err := srv.store.Load(testSandboxID); err == nil {
		t.Error("record survived canceled create")
	}
	srv.mu.Lock()
	_, ok := srv.sandboxes[testSandboxID]
	srv.mu.Unlock()
	if ok {
		t.Error("registry entry survived canceled create")
	}
	close(fake.RunBlock)
}
