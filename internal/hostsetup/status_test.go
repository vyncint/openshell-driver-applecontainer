package hostsetup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vyncint/openshell-driver-applecontainer/internal/backend"
	computev1 "github.com/vyncint/openshell-driver-applecontainer/internal/gen/computev1"
)

// statusHost simulates the host commands status shells out to.
type statusHost struct {
	containerVersion string // "" = not installed
	gatewayVersion   string
	agentLoaded      bool
	agentPID         string
}

func (h *statusHost) exec(name string, args ...string) (string, error) {
	switch name {
	case "container":
		if h.containerVersion == "" {
			return "", errors.New("not found")
		}
		return "container CLI version " + h.containerVersion + " (build: release)\n", nil
	case "openshell-gateway":
		if h.gatewayVersion == "" {
			return "", errors.New("not found")
		}
		return "openshell-gateway " + h.gatewayVersion + "\n", nil
	case "launchctl":
		if len(args) > 0 && args[0] == "print" {
			if !h.agentLoaded {
				return "Could not find service", errors.New("exit 113")
			}
			out := "state = running\n"
			if h.agentPID != "" {
				out += "\tpid = " + h.agentPID + "\n"
			}
			return out, nil
		}
	}
	return "", nil
}

func newStatusSetup(t *testing.T, host *statusHost, fake *backend.Fake) *Setup {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	// Kernel configured, plist installed, gateway.env wired.
	kernel := (&Setup{Home: home}).defaultKernelPath()
	if err := os.MkdirAll(filepath.Dir(kernel), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kernel, []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &Setup{
		RT:           fake,
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		Home:         home,
		UID:          501,
		Exec:         host.exec,
		HasOpenShell: func() bool { return true },
	}
}

func find(checks []Check, name string) []Check {
	var out []Check
	for _, c := range checks {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

func TestStatusReportsMissingPieces(t *testing.T) {
	host := &statusHost{containerVersion: "1.3.0", gatewayVersion: "0.0.116"}
	fake := &backend.Fake{}
	s := newStatusSetup(t, host, fake)
	socket := filepath.Join(t.TempDir(), "driver.sock")

	checks := s.Status(context.Background(), StatusOptions{
		Network: "oshl", Socket: socket, TLSDir: t.TempDir(),
		SupervisorImage: "ghcr.io/nvidia/openshell/supervisor:0.0.113",
	})
	if Worst(checks) != "fail" {
		t.Fatalf("an unwired host must fail, got %s: %+v", Worst(checks), checks)
	}
	// apple/container 1.3.0: installed, but carries the advisory warning.
	ac := find(checks, "apple/container")
	if len(ac) != 2 || ac[0].Level != "ok" || ac[1].Level != "warn" || !strings.Contains(ac[1].Detail, "security advisories") {
		t.Errorf("apple/container checks = %+v", ac)
	}
	for name, wantSub := range map[string]string{
		"vmnet network":       "does not exist",
		"gateway wiring":      "missing",
		"gateway certificate": "not readable",
		"driver service":      "not installed",
		"driver socket":       "not answering",
		"supervisor image":    "does not match",
	} {
		got := find(checks, name)
		if len(got) == 0 || got[0].Level == "ok" || !strings.Contains(got[0].Detail, wantSub) {
			t.Errorf("%s = %+v, want non-ok containing %q", name, got, wantSub)
		}
	}
	if len(find(checks, "sandboxes")) != 0 {
		t.Error("sandbox summary must be absent when the driver does not answer")
	}

	var buf bytes.Buffer
	PrintChecks(&buf, checks)
	if !strings.Contains(buf.String(), "FAIL") || !strings.Contains(buf.String(), "Problems found") {
		t.Errorf("rendered output:\n%s", buf.String())
	}
}

func TestStatusHealthyHost(t *testing.T) {
	host := &statusHost{containerVersion: "1.4.1", gatewayVersion: "0.0.116", agentLoaded: true, agentPID: "4242"}
	fake := &backend.Fake{}
	fake.AddNetwork(backend.Network{Name: "oshl", IPv4Gateway: "192.168.64.1"})
	s := newStatusSetup(t, host, fake)

	// Plist, gateway.env and a certificate carrying the vmnet SAN.
	if err := os.MkdirAll(filepath.Dir(s.agentPlistPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.agentPlistPath(), []byte("plist"), 0o600); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(shortTempDir(t), "d.sock")
	envPath := filepath.Join(s.configDir(), "gateway.env")
	if err := upsertFileBlock(envPath, GatewayEnvLines(socket, "/tls")); err != nil {
		t.Fatal(err)
	}
	tlsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tlsDir, "server"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tlsDir, "server", "tls.crt"), selfSignedCert(t, "192.168.64.1"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A live driver on the socket.
	stop := serveFakeDriver(t, socket, "0.3.0", []*computev1.DriverSandbox{
		{Id: "a", Status: &computev1.DriverSandboxStatus{Conditions: []*computev1.DriverCondition{{Type: "Ready", Status: "True"}}}},
		{Id: "b", Status: &computev1.DriverSandboxStatus{Conditions: []*computev1.DriverCondition{{Type: "Ready", Status: "False", Reason: "ContainerStopped"}}}},
	})
	defer stop()

	checks := s.Status(context.Background(), StatusOptions{
		Network: "oshl", Socket: socket, TLSDir: tlsDir,
		SupervisorImage: "ghcr.io/nvidia/openshell/supervisor:0.0.116",
		DriverVersion:   "0.3.0",
	})
	// The gateway listener check depends on a real port on this machine;
	// ignore it and require everything else to be clean.
	var rest []Check
	for _, c := range checks {
		if c.Name != "OpenShell gateway" {
			rest = append(rest, c)
		}
	}
	if Worst(rest) != "ok" {
		t.Errorf("healthy host reported problems: %+v", rest)
	}
	if sb := find(checks, "sandboxes"); len(sb) != 1 || sb[0].Detail != "1 running, 1 stopped" {
		t.Errorf("sandbox summary = %+v", sb)
	}
	if svc := find(checks, "driver service"); len(svc) != 1 || !strings.Contains(svc[0].Detail, "pid 4242") {
		t.Errorf("driver service = %+v", svc)
	}

	// Binary newer than the running service: a warning that names setup.
	checks = s.Status(context.Background(), StatusOptions{
		Network: "oshl", Socket: socket, TLSDir: tlsDir, DriverVersion: "0.3.1",
	})
	if sock := find(checks, "driver socket"); len(sock) != 1 || sock[0].Level != "warn" || !strings.Contains(sock[0].Detail, "run setup") {
		t.Errorf("stale service should warn, got %+v", sock)
	}
}

func TestLaunchctlPIDAndWorst(t *testing.T) {
	if got := launchctlPID("gui/501/x = {\n\tactive count = 1\n\tpid = 123\n\tstate = running\n}"); got != "123" {
		t.Errorf("pid = %q", got)
	}
	if got := launchctlPID("state = not running\n"); got != "" {
		t.Errorf("pid without process = %q", got)
	}
	if Worst(nil) != "ok" || Worst([]Check{{Level: "ok"}, {Level: "warn"}}) != "warn" || Worst([]Check{{Level: "warn"}, {Level: "fail"}}) != "fail" {
		t.Error("Worst ordering broken")
	}
}
