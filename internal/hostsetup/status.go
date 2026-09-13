package hostsetup

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/vyncint/openshell-driver-applecontainer/internal/compat"
	computev1 "github.com/vyncint/openshell-driver-applecontainer/internal/gen/computev1"
)

// Check is one line of `status` output.
type Check struct {
	// Name is the component, e.g. "driver service".
	Name string `json:"name"`
	// Level is "ok", "warn" or "fail".
	Level string `json:"level"`
	// Detail is the one-line human explanation.
	Detail string `json:"detail"`
}

// StatusOptions configures a status run; zero values use the driver defaults
// (the same ones setup applied).
type StatusOptions struct {
	Network         string
	Socket          string
	TLSDir          string // "" = auto-detect
	SupervisorImage string
	DriverVersion   string // this binary's version, for the driver/service comparison
}

// gatewayAddr is where the stock gateway listens for the local CLI.
const gatewayAddr = "127.0.0.1:17670"

// Status inspects every piece setup wires together and reports what it
// finds, without changing anything. It is the first thing to run when
// "something looks broken": each line names the component, whether it is
// healthy, and what to do about it when it is not.
func (s *Setup) Status(ctx context.Context, opts StatusOptions) []Check {
	var out []Check
	add := func(name, level, format string, args ...any) {
		out = append(out, Check{Name: name, Level: level, Detail: fmt.Sprintf(format, args...)})
	}

	// apple/container: CLI present, version verdict, runtime up, kernel set.
	acVer := probeVersion(s.Exec, "container")
	if acVer == "" {
		add("apple/container", "fail", "the `container` CLI is not installed or not on PATH; install it from https://github.com/apple/container/releases")
	} else {
		if _, err := s.RT.Networks(ctx); err != nil {
			add("apple/container", "fail", "%s installed but the runtime is not running: `container system start` (setup does this too)", acVer)
		} else {
			add("apple/container", "ok", "%s, runtime running", acVer)
		}
		for _, f := range compat.CheckAppleContainer(acVer) {
			if f.Level != "ok" {
				add("apple/container", mapLevel(f.Level), "%s", f.Message)
			}
		}
		if _, err := os.Lstat(s.defaultKernelPath()); err != nil {
			add("guest kernel", "fail", "no default guest kernel is configured, so no sandbox can boot; run setup (or `container system kernel set --recommended`)")
		} else {
			add("guest kernel", "ok", "default kernel configured")
		}
	}

	// vmnet network and the address guests dial.
	gatewayIP := ""
	if networks, err := s.RT.Networks(ctx); err == nil {
		for _, n := range networks {
			if n.Name == opts.Network {
				gatewayIP = n.IPv4Gateway
			}
		}
		if gatewayIP == "" {
			add("vmnet network", "fail", "network %q does not exist; run setup to create it", opts.Network)
		} else {
			add("vmnet network", "ok", "%s, host address %s", opts.Network, gatewayIP)
		}
	}

	// OpenShell gateway: binary, version verdict, service, listener.
	gwVer := probeVersion(s.Exec, "openshell-gateway")
	if gwVer == "" {
		add("OpenShell gateway", "fail", "openshell-gateway is not installed or not on PATH; `brew install nvidia/openshell/openshell`")
	} else {
		for _, f := range compat.CheckOpenShell(gwVer) {
			if f.Level != "ok" {
				add("OpenShell gateway", mapLevel(f.Level), "%s", f.Message)
			}
		}
		switch {
		case dialOK("tcp", gatewayAddr):
			add("OpenShell gateway", "ok", "%s, listening on %s", gwVer, gatewayAddr)
		case s.openShellInstalled():
			add("OpenShell gateway", "fail", "%s installed but nothing listens on %s; `brew services restart openshell` (setup does this) and check /opt/homebrew/var/log/openshell/openshell-gateway.err.log", gwVer, gatewayAddr)
		default:
			add("OpenShell gateway", "warn", "%s present but not installed through Homebrew; nothing listens on %s — start openshell-gateway yourself", gwVer, gatewayAddr)
		}
	}

	// gateway.env managed block.
	envPath := filepath.Join(s.configDir(), "gateway.env")
	if data, err := os.ReadFile(envPath); err != nil {
		add("gateway wiring", "fail", "%s is missing; the gateway service is not configured to use this driver — run setup", envPath)
	} else if !strings.Contains(string(data), blockBegin) || !strings.Contains(string(data), "OPENSHELL_DRIVERS=applecontainer") {
		add("gateway wiring", "fail", "%s has no managed block selecting the applecontainer driver — run setup", envPath)
	} else if !strings.Contains(string(data), "OPENSHELL_COMPUTE_DRIVER_SOCKET="+shellQuote(opts.Socket)) {
		add("gateway wiring", "warn", "%s points the gateway at a different driver socket than %s — run setup to realign", envPath, opts.Socket)
	} else {
		add("gateway wiring", "ok", "%s selects the applecontainer driver at %s", envPath, opts.Socket)
	}

	// TLS bundle and the SAN guests verify.
	tlsDir := opts.TLSDir
	if tlsDir == "" {
		tlsDir = s.detectTLSDir()
	}
	certPath := filepath.Join(tlsDir, "server", "tls.crt")
	switch data, err := os.ReadFile(certPath); {
	case err != nil:
		add("gateway certificate", "fail", "%s is not readable; install OpenShell (its post-install generates the bundle) or run setup", certPath)
	case gatewayIP == "":
		add("gateway certificate", "warn", "%s present; cannot verify its SAN without the vmnet address", certPath)
	default:
		ok, serr := CertHasIPSAN(data, gatewayIP)
		switch {
		case serr != nil:
			add("gateway certificate", "fail", "%s does not parse: %v", certPath, serr)
		case !ok:
			add("gateway certificate", "fail", "%s has no SAN for %s, so guests cannot verify the gateway — run setup to regenerate", certPath, gatewayIP)
		default:
			add("gateway certificate", "ok", "SAN covers %s (%s)", gatewayIP, tlsDir)
		}
	}

	// Driver launchd service.
	target := fmt.Sprintf("gui/%d/%s", s.UID, AgentLabel)
	if _, err := os.Stat(s.agentPlistPath()); err != nil {
		add("driver service", "fail", "%s is not installed as a launchd agent — run setup", AgentLabel)
	} else if out, err := s.Exec("launchctl", "print", target); err != nil {
		add("driver service", "fail", "%s is installed but not loaded (Homebrew unloads it on every `brew upgrade`) — run setup", AgentLabel)
	} else if pid := launchctlPID(out); pid == "" {
		add("driver service", "fail", "%s is loaded but not running; see %s", AgentLabel, s.agentLogPath())
	} else {
		add("driver service", "ok", "%s running (pid %s), log %s", AgentLabel, pid, s.agentLogPath())
	}

	// Driver socket: a real GetCapabilities round trip, as the gateway does.
	caps, sandboxes, err := probeDriver(ctx, opts.Socket)
	switch {
	case err != nil:
		add("driver socket", "fail", "%s is not answering (%v) — run setup, then see %s", opts.Socket, err, s.agentLogPath())
	default:
		detail := fmt.Sprintf("%s answering, driver %s", opts.Socket, caps.GetDriverVersion())
		if opts.DriverVersion != "" && caps.GetDriverVersion() != opts.DriverVersion {
			add("driver socket", "warn", "%s — but this binary is %s; the service is still on the old build, run setup to restart it", detail, opts.DriverVersion)
		} else {
			add("driver socket", "ok", "%s", detail)
		}
		add("sandboxes", "ok", "%s", summarizeSandboxes(sandboxes))
	}

	// Supervisor image tag versus gateway.
	if gwVer != "" && opts.SupervisorImage != "" {
		tag := opts.SupervisorImage[strings.LastIndex(opts.SupervisorImage, ":")+1:]
		if tag != gwVer {
			add("supervisor image", "warn", "%s does not match gateway %s; sandboxes may fail to connect (unset --supervisor-image to match automatically)", opts.SupervisorImage, gwVer)
		} else {
			add("supervisor image", "ok", "%s matches the gateway", opts.SupervisorImage)
		}
	}
	return out
}

// Worst returns the most severe level present: "fail" > "warn" > "ok".
func Worst(checks []Check) string {
	worst := "ok"
	for _, c := range checks {
		switch c.Level {
		case "fail":
			return "fail"
		case "warn":
			worst = "warn"
		}
	}
	return worst
}

// PrintChecks renders checks for a terminal.
func PrintChecks(w io.Writer, checks []Check) {
	marks := map[string]string{"ok": "OK  ", "warn": "WARN", "fail": "FAIL"}
	width := 0
	for _, c := range checks {
		if len(c.Name) > width {
			width = len(c.Name)
		}
	}
	for _, c := range checks {
		_, _ = fmt.Fprintf(w, "  %s  %-*s  %s\n", marks[c.Level], width, c.Name, c.Detail)
	}
	switch Worst(checks) {
	case "ok":
		_, _ = fmt.Fprintln(w, "\nAll checks passed.")
	case "warn":
		_, _ = fmt.Fprintln(w, "\nWorking, with warnings above.")
	default:
		_, _ = fmt.Fprintln(w, "\nProblems found. `openshell-driver-applecontainer setup` repairs the wiring; the lines above say what else to do.")
	}
}

func mapLevel(compatLevel string) string {
	if compatLevel == "error" {
		return "fail"
	}
	return compatLevel
}

func dialOK(network, addr string) bool {
	conn, err := net.DialTimeout(network, addr, time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// launchctlPID pulls the pid out of `launchctl print` output ("" when the
// service is loaded but has no process).
func launchctlPID(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "pid = ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "pid = "))
		}
	}
	return ""
}

// probeDriver dials the driver socket and performs the same handshake the
// gateway does, then lists sandboxes.
func probeDriver(ctx context.Context, socket string) (*computev1.GetCapabilitiesResponse, []*computev1.DriverSandbox, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := grpc.NewClient("unix:"+socket, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = conn.Close() }()
	client := computev1.NewComputeDriverClient(conn)
	caps, err := client.GetCapabilities(ctx, &computev1.GetCapabilitiesRequest{})
	if err != nil {
		return nil, nil, err
	}
	list, err := client.ListSandboxes(ctx, &computev1.ListSandboxesRequest{})
	if err != nil {
		return caps, nil, nil // capabilities answered; the list is a bonus
	}
	return caps, list.GetSandboxes(), nil
}

func summarizeSandboxes(sbs []*computev1.DriverSandbox) string {
	if len(sbs) == 0 {
		return "none"
	}
	counts := map[string]int{}
	for _, sb := range sbs {
		key := "provisioning"
		if sb.GetStatus().GetDeleting() {
			key = "deleting"
		} else if conds := sb.GetStatus().GetConditions(); len(conds) > 0 {
			switch c := conds[0]; {
			case c.GetStatus() == "True":
				key = "running"
			case c.GetReason() == "ContainerStopped":
				key = "stopped"
			case c.GetReason() == "Starting":
				key = "provisioning"
			default:
				key = "failed"
			}
		}
		counts[key]++
	}
	var parts []string
	for _, k := range []string{"running", "stopped", "provisioning", "failed", "deleting"} {
		if n := counts[k]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, k))
		}
	}
	return strings.Join(parts, ", ")
}
