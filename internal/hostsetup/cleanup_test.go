package hostsetup

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cmdRec records every Exec/ExecStream invocation without running anything, so
// cleanup's shell-outs can be asserted safely and deterministically.
type cmdRec struct{ calls []string }

func (r *cmdRec) exec(name string, args ...string) (string, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	return "", nil
}
func (r *cmdRec) stream(name string, args ...string) error {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	return nil
}
func (r *cmdRec) ran(substr string) bool {
	for _, c := range r.calls {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

func newCleanupSetup(t *testing.T, rec *cmdRec, hasOpenShell bool) *Setup {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", "") // force the Home-based config dir
	home := t.TempDir()
	s := &Setup{
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		Home:         home,
		UID:          501,
		Exec:         rec.exec,
		ExecStream:   rec.stream,
		HasOpenShell: func() bool { return hasOpenShell },
		// No real system paths: apple/container step is exercised via a temp
		// stub; the OpenShell var dir is disabled.
		OpenShellVarDir: "",
	}
	// A plist and a managed gateway.env so the removal paths run.
	if err := os.MkdirAll(filepath.Dir(s.agentPlistPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.agentPlistPath(), []byte("plist"), 0o600); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(s.configDir(), "gateway.env")
	if err := os.MkdirAll(filepath.Dir(envPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envPath, []byte(UpsertManagedBlock("", []string{"OPENSHELL_DRIVERS=x"})), 0o600); err != nil {
		t.Fatal(err)
	}
	return s
}

// Bare cleanup removes only the driver service + gateway wiring; it must not
// touch data or prerequisites.
func TestCleanupKeepDataDriverOnly(t *testing.T) {
	rec := &cmdRec{}
	s := newCleanupSetup(t, rec, true)

	if err := s.Cleanup(CleanupOptions{
		Network: "oshl", Socket: filepath.Join(t.TempDir(), "sock", "driver.sock"),
		StateDir: t.TempDir(), DefaultImage: "base:latest", SupervisorImage: "sup:0.0.96",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(s.agentPlistPath()); !os.IsNotExist(err) {
		t.Errorf("plist should be removed, err=%v", err)
	}
	if !rec.ran("launchctl bootout") {
		t.Error("expected launchctl bootout")
	}
	if !rec.ran("brew services stop openshell") {
		t.Error("expected the gateway to be stopped")
	}
	for _, forbidden := range []string{"container image rm", "container network rm", "brew uninstall", "uninstall-container"} {
		if rec.ran(forbidden) {
			t.Errorf("keep-data/driver-only cleanup must not run %q; calls=%v", forbidden, rec.calls)
		}
	}
}

// -d removes the driver's own data (state dir, socket dir, images, network)
// but not the prerequisites.
func TestCleanupDeleteDataRemovesDriverData(t *testing.T) {
	rec := &cmdRec{}
	s := newCleanupSetup(t, rec, true)

	stateDir := t.TempDir()
	sockDir := t.TempDir()
	socket := filepath.Join(sockDir, "driver.sock")
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := s.Cleanup(CleanupOptions{
		DeleteData: true,
		Network:    "oshl", Socket: socket, StateDir: stateDir,
		DefaultImage: "ghcr.io/x/base:latest", SupervisorImage: "ghcr.io/x/sup:0.0.96",
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Errorf("state dir should be removed, err=%v", err)
	}
	if _, err := os.Stat(sockDir); !os.IsNotExist(err) {
		t.Errorf("socket dir should be removed, err=%v", err)
	}
	for _, want := range []string{
		"container image rm ghcr.io/x/base:latest",
		"container image rm ghcr.io/x/sup:0.0.96",
		"container network rm oshl",
	} {
		if !rec.ran(want) {
			t.Errorf("expected %q; calls=%v", want, rec.calls)
		}
	}
	if rec.ran("brew uninstall") {
		t.Error("-d without --all must not uninstall OpenShell")
	}
}

// --all removes the prerequisites: brew uninstall + apple/container's own
// uninstaller (with the -k/-d flag matching the data choice).
func TestCleanupAllRemovesPrerequisites(t *testing.T) {
	rec := &cmdRec{}
	s := newCleanupSetup(t, rec, true)
	// Stub apple/container's uninstaller so the Stat check passes.
	acStub := filepath.Join(t.TempDir(), "uninstall-container.sh")
	if err := os.WriteFile(acStub, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.ACUninstaller = acStub

	if err := s.Cleanup(CleanupOptions{All: true, Network: "oshl"}); err != nil {
		t.Fatal(err)
	}
	if !rec.ran("brew uninstall openshell") {
		t.Errorf("expected brew uninstall openshell; calls=%v", rec.calls)
	}
	if !rec.ran("container system stop") {
		t.Errorf("expected the runtime to be stopped before uninstall; calls=%v", rec.calls)
	}
	if !rec.ran(acStub + " -k") {
		t.Errorf("expected apple/container uninstaller with -k (keep data); calls=%v", rec.calls)
	}
}

// --all -d passes -d to apple/container's uninstaller.
func TestCleanupAllDeleteDataPassesDeleteFlag(t *testing.T) {
	rec := &cmdRec{}
	s := newCleanupSetup(t, rec, true)
	acStub := filepath.Join(t.TempDir(), "uninstall-container.sh")
	if err := os.WriteFile(acStub, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.ACUninstaller = acStub

	if err := s.Cleanup(CleanupOptions{All: true, DeleteData: true, Network: "oshl", StateDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if !rec.ran(acStub + " -d") {
		t.Errorf("expected apple/container uninstaller with -d (delete data); calls=%v", rec.calls)
	}
}

// A fresh apple/container install (or one whose user data was deleted by
// `cleanup --all -d`) has no default kernel, and then every sandbox create
// fails at image unpack. setup must install one.
func TestEnsureKernelInstallsWhenMissing(t *testing.T) {
	rec := &cmdRec{}
	s := newCleanupSetup(t, rec, true) // Home is a temp dir: no kernel present

	s.ensureKernel()

	if !rec.ran("container system kernel set --recommended") {
		t.Errorf("expected the recommended kernel to be installed; calls=%v", rec.calls)
	}
}

// When a default kernel is already configured, setup must not re-download it.
func TestEnsureKernelSkipsWhenPresent(t *testing.T) {
	rec := &cmdRec{}
	s := newCleanupSetup(t, rec, true)

	kernelPath := s.defaultKernelPath()
	if err := os.MkdirAll(filepath.Dir(kernelPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kernelPath, []byte("kernel"), 0o644); err != nil {
		t.Fatal(err)
	}

	s.ensureKernel()

	if rec.ran("kernel set") {
		t.Errorf("a configured kernel must not be reinstalled; calls=%v", rec.calls)
	}
}

// With OpenShell absent, cleanup skips the gateway/brew steps but still
// removes the driver service.
func TestCleanupWithoutOpenShell(t *testing.T) {
	rec := &cmdRec{}
	s := newCleanupSetup(t, rec, false)

	if err := s.Cleanup(CleanupOptions{}); err != nil {
		t.Fatal(err)
	}
	if rec.ran("brew") {
		t.Errorf("no brew calls expected when OpenShell is absent; calls=%v", rec.calls)
	}
	if !rec.ran("launchctl bootout") {
		t.Error("driver service should still be removed")
	}
}
