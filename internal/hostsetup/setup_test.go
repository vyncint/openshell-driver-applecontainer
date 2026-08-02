package hostsetup

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newInstallSetup(t *testing.T) (*Setup, string) {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "driver.sock")
	return &Setup{
		Log:          slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		Home:         t.TempDir(),
		UID:          501,
		BinPath:      "/opt/homebrew/bin/openshell-driver-applecontainer",
		readyTimeout: 300 * time.Millisecond,
	}, socket
}

// launchdSim is a minimal launchctl double: a registered/loaded flag plus
// hooks for what bootstrap and kickstart do to the (simulated) socket.
type launchdSim struct {
	mu          sync.Mutex
	loaded      bool
	calls       []string
	onBootstrap func()
	onKickstart func()
}

func (l *launchdSim) exec(_ string, args ...string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, strings.Join(args, " "))
	switch args[0] {
	case "print":
		if l.loaded {
			return "", nil
		}
		return "Could not find service", errors.New("exit 113")
	case "bootout":
		l.loaded = false
		return "", nil
	case "bootstrap":
		l.loaded = true
		if l.onBootstrap != nil {
			l.onBootstrap()
		}
		return "", nil
	case "kickstart":
		if l.onKickstart != nil {
			l.onKickstart()
		}
		return "", nil
	}
	return "", nil
}

func (l *launchdSim) called(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range l.calls {
		if strings.HasPrefix(c, sub) {
			return true
		}
	}
	return false
}

func TestInstallAgentStartsCleanly(t *testing.T) {
	s, socket := newInstallSetup(t)
	sim := &launchdSim{onBootstrap: func() { _ = os.WriteFile(socket, nil, 0o600) }}
	s.Exec = sim.exec

	if err := s.installAgent("/tls", socket); err != nil {
		t.Fatal(err)
	}
	if sim.called("kickstart") {
		t.Error("kickstart should not run when bootstrap brings the service up")
	}
	info, err := os.Stat(s.agentPlistPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("plist perms = %o, want 600", info.Mode().Perm())
	}
}

func TestInstallAgentWaitsForUnloadThenBootstraps(t *testing.T) {
	s, socket := newInstallSetup(t)
	// Service starts loaded (an old instance). bootout unloads it; the
	// unload-wait must let bootstrap proceed cleanly.
	sim := &launchdSim{loaded: true, onBootstrap: func() { _ = os.WriteFile(socket, nil, 0o600) }}
	s.Exec = sim.exec

	if err := s.installAgent("/tls", socket); err != nil {
		t.Fatal(err)
	}
	if !sim.called("bootout") || !sim.called("bootstrap") {
		t.Errorf("expected bootout then bootstrap, calls: %v", sim.calls)
	}
}

func TestInstallAgentKickstartsWhenServiceDoesNotComeUp(t *testing.T) {
	s, socket := newInstallSetup(t)
	// bootstrap registers the label but the process never listens (the
	// upgrade-bounce race); only the forced kickstart brings it up.
	sim := &launchdSim{onKickstart: func() { _ = os.WriteFile(socket, nil, 0o600) }}
	s.Exec = sim.exec

	if err := s.installAgent("/tls", socket); err != nil {
		t.Fatalf("installAgent should recover via kickstart: %v", err)
	}
	if !sim.called("kickstart") {
		t.Errorf("expected a kickstart retry, calls: %v", sim.calls)
	}
}

func TestInstallAgentFailsLoudlyWhenServiceNeverStarts(t *testing.T) {
	s, socket := newInstallSetup(t)
	sim := &launchdSim{} // nothing ever creates the socket
	s.Exec = sim.exec

	err := s.installAgent("/tls", socket)
	if err == nil || !strings.Contains(err.Error(), "did not start") {
		t.Errorf("want a loud failure when the service never starts, got %v", err)
	}
	if !sim.called("kickstart") {
		t.Error("should have attempted a kickstart before giving up")
	}
}
