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
	fake.AddImage(testImage, "sha256:baseimg")
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
	if boot.Image != testImage || boot.Network != "oshl" {
		t.Errorf("boot image/network = %q/%q", boot.Image, boot.Network)
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
		"openshell-sandbox": 0o755,
		"boot.sh":           0o755,
		"tls/ca.crt":        0o644,
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
