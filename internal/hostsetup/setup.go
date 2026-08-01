package hostsetup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/vyncint/openshell-driver-applecontainer/internal/backend"
)

// Options configures a setup run.
type Options struct {
	Network         string
	Socket          string
	TLSDir          string // "" = auto-detect
	DefaultImage    string
	SupervisorImage string
	PullImages      bool
}

// Setup orchestrates the host installation. Exec runs a command and
// captures output; ExecStream attaches the command to the user's terminal
// (progress bars for pulls, brew output).
type Setup struct {
	RT         backend.Runtime
	Log        *slog.Logger
	Home       string
	UID        int
	BinPath    string
	Exec       func(name string, args ...string) (string, error)
	ExecStream func(name string, args ...string) error
}

// New builds a Setup wired to the real host.
func New(rt backend.Runtime, log *slog.Logger) (*Setup, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	bin, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve driver binary path: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(bin); err == nil {
		bin = resolved
	}
	return &Setup{
		RT:      rt,
		Log:     log,
		Home:    home,
		UID:     os.Getuid(),
		BinPath: bin,
		Exec: func(name string, args ...string) (string, error) {
			out, err := exec.Command(name, args...).CombinedOutput()
			return string(out), err
		},
		ExecStream: func(name string, args ...string) error {
			cmd := exec.Command(name, args...)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd.Run()
		},
	}, nil
}

func (s *Setup) configDir() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "openshell")
	}
	return filepath.Join(s.Home, ".config", "openshell")
}

func (s *Setup) agentPlistPath() string {
	return filepath.Join(s.Home, "Library", "LaunchAgents", AgentLabel+".plist")
}

func (s *Setup) agentLogPath() string {
	return filepath.Join(s.Home, "Library", "Logs", "openshell-driver-applecontainer.log")
}

// Run performs the full idempotent setup.
func (s *Setup) Run(ctx context.Context, opts Options) error {
	step := func(msg string, args ...any) { s.Log.Info("setup: "+msg, args...) }

	// 1. Container runtime up.
	if _, err := s.RT.Networks(ctx); err != nil {
		step("starting the container runtime")
		if err := s.RT.SystemStart(ctx); err != nil {
			return fmt.Errorf("the apple/container runtime is not available (install it from https://github.com/apple/container): %w", err)
		}
	}

	// 2. vmnet network + guest-reachable gateway address.
	gatewayIP, err := s.ensureNetwork(ctx, opts.Network)
	if err != nil {
		return err
	}
	step("vmnet network ready", "network", opts.Network, "gateway_ip", gatewayIP)

	// 3. Gateway TLS bundle with a SAN for the vmnet address.
	tlsDir := opts.TLSDir
	if tlsDir == "" {
		tlsDir = s.detectTLSDir()
	}
	if err := s.ensureCertSAN(tlsDir, gatewayIP); err != nil {
		return err
	}
	step("gateway certificate covers the vmnet address", "tls_dir", tlsDir, "san", gatewayIP)

	// 4. gateway.env managed block: the stock Homebrew openshell service
	// sources this file, so the standard service picks up the driver.
	envPath := filepath.Join(s.configDir(), "gateway.env")
	if err := upsertFileBlock(envPath, GatewayEnvLines(opts.Socket, tlsDir)); err != nil {
		return fmt.Errorf("write %s: %w", envPath, err)
	}
	step("gateway service configured to use the driver", "file", envPath)

	// 5. Repair a CLI registration pointing at IPv6 loopback.
	s.fixCLIRegistrations()

	// 6. The driver itself as a launchd agent (starts at login, restarts
	// on exit).
	if err := s.installAgent(tlsDir, opts.Socket); err != nil {
		return err
	}
	if waitFor(func() bool { _, err := os.Stat(opts.Socket); return err == nil }, 15*time.Second) {
		step("driver service running", "socket", opts.Socket)
	} else {
		s.Log.Warn("driver service did not come up in time; check the log", "log", s.agentLogPath())
	}

	// 7. Restart the gateway service so it dials the driver.
	s.restartGatewayService()

	// 8. Optionally pre-pull the images so the first sandbox is fast.
	if opts.PullImages {
		s.pullImages(ctx, opts)
	} else {
		step("skipping image pre-pull; the first sandbox create will pull " + opts.DefaultImage)
	}

	fmt.Printf("\nSetup complete. Try:\n\n    openshell sandbox create --name demo\n    openshell sandbox exec -n demo -- uname -a\n    openshell sandbox delete demo\n\nRe-run `%s setup` any time to repair the installation.\n", filepath.Base(s.BinPath))
	return nil
}

// Uninstall removes what setup installed (certificates, network, and images
// are left in place — they are harmless and expensive to recreate).
func (s *Setup) Uninstall() error {
	target := fmt.Sprintf("gui/%d/%s", s.UID, AgentLabel)
	if out, err := s.Exec("launchctl", "bootout", target); err != nil {
		s.Log.Debug("launchctl bootout", "out", out, "err", err)
	}
	if err := os.Remove(s.agentPlistPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	s.Log.Info("uninstall: driver service removed")

	envPath := filepath.Join(s.configDir(), "gateway.env")
	if data, err := os.ReadFile(envPath); err == nil {
		rest := RemoveManagedBlock(string(data))
		if len(rest) == 0 || rest == "\n" {
			_ = os.Remove(envPath)
		} else if err := os.WriteFile(envPath, []byte(rest), 0o600); err != nil {
			return err
		}
		s.Log.Info("uninstall: gateway service configuration removed", "file", envPath)
	}

	if s.brewHasOpenShell() {
		s.Log.Info("uninstall: stopping the gateway service (it has no compute driver configured anymore)")
		if err := s.ExecStream("brew", "services", "stop", "openshell"); err != nil {
			s.Log.Warn("brew services stop openshell failed", "err", err)
		}
	}
	s.Log.Info("uninstall: done (certificates, vmnet network and images were kept)")
	return nil
}

func (s *Setup) ensureNetwork(ctx context.Context, name string) (string, error) {
	find := func() (string, error) {
		networks, err := s.RT.Networks(ctx)
		if err != nil {
			return "", err
		}
		for _, n := range networks {
			if n.Name == name {
				return n.IPv4Gateway, nil
			}
		}
		return "", nil
	}
	ip, err := find()
	if err != nil {
		return "", fmt.Errorf("list vmnet networks: %w", err)
	}
	if ip != "" {
		return ip, nil
	}
	if err := s.RT.NetworkCreate(ctx, name); err != nil {
		return "", fmt.Errorf("create vmnet network %s: %w", name, err)
	}
	ip, err = find()
	if err != nil || ip == "" {
		return "", fmt.Errorf("vmnet network %s has no gateway address after creation (%v)", name, err)
	}
	return ip, nil
}

// detectTLSDir mirrors the driver's guest-TLS default resolution.
func (s *Setup) detectTLSDir() string {
	if v := os.Getenv("OPENSHELL_LOCAL_TLS_DIR"); v != "" {
		return v
	}
	xdg := filepath.Join(s.Home, ".local", "state", "openshell", "tls")
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		xdg = filepath.Join(v, "openshell", "tls")
	}
	for _, dir := range []string{"/opt/homebrew/var/openshell/tls", xdg} {
		if _, err := os.Stat(filepath.Join(dir, "ca.crt")); err == nil {
			return dir
		}
	}
	return xdg
}

func (s *Setup) ensureCertSAN(tlsDir, ip string) error {
	certPath := filepath.Join(tlsDir, "server", "tls.crt")
	if data, err := os.ReadFile(certPath); err == nil {
		if ok, err := CertHasIPSAN(data, ip); err == nil && ok {
			return nil
		}
	}
	if _, err := exec.LookPath("openshell-gateway"); err != nil {
		return fmt.Errorf("the gateway certificate at %s does not cover %s and openshell-gateway is not on PATH to regenerate it; install OpenShell first (https://github.com/NVIDIA/OpenShell)", certPath, ip)
	}
	s.Log.Info("setup: refreshing gateway certificate SANs (CA is preserved)", "san", ip)
	out, err := s.Exec("openshell-gateway", "generate-certs",
		"--output-dir", tlsDir,
		"--server-san", "host.openshell.internal",
		"--server-san", ip)
	if err != nil {
		return fmt.Errorf("generate-certs failed: %v: %s", err, out)
	}
	return nil
}

func (s *Setup) fixCLIRegistrations() {
	pattern := filepath.Join(s.configDir(), "gateways", "*", "metadata.json")
	matches, _ := filepath.Glob(pattern)
	for _, p := range matches {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		fixed, changed, err := FixLocalGatewayMetadata(data)
		if err != nil || !changed {
			continue
		}
		if err := os.WriteFile(p, fixed, 0o600); err == nil {
			s.Log.Info("setup: fixed CLI gateway registration (IPv6 loopback is unreachable with an IPv4 bind)", "file", p)
		}
	}
}

func (s *Setup) installAgent(tlsDir, socket string) error {
	logPath := s.agentLogPath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	plistPath := s.agentPlistPath()
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return err
	}
	plist := RenderLaunchAgent(s.BinPath, tlsDir, logPath)
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return err
	}
	target := fmt.Sprintf("gui/%d/%s", s.UID, AgentLabel)
	if out, err := s.Exec("launchctl", "bootout", target); err != nil {
		s.Log.Debug("launchctl bootout (fresh install is fine)", "out", out, "err", err)
	} else {
		// A previous instance drains RPCs for up to 10s and removes its
		// socket on exit; wait for that so the fresh instance never races
		// the old one for the socket.
		waitFor(func() bool { _, err := os.Stat(socket); return err != nil }, 15*time.Second)
	}
	if out, err := s.Exec("launchctl", "bootstrap", "gui/"+strconv.Itoa(s.UID), plistPath); err != nil {
		return fmt.Errorf("launchctl bootstrap failed: %v: %s", err, out)
	}
	s.Log.Info("setup: driver installed as a launchd service", "plist", plistPath, "binary", s.BinPath, "log", logPath)
	return nil
}

func (s *Setup) brewHasOpenShell() bool {
	if _, err := exec.LookPath("brew"); err != nil {
		return false
	}
	_, err := os.Stat("/opt/homebrew/opt/openshell")
	return err == nil
}

func (s *Setup) restartGatewayService() {
	if !s.brewHasOpenShell() {
		s.Log.Warn("setup: Homebrew OpenShell service not found; start the gateway manually",
			"hint", "openshell-gateway (it reads "+filepath.Join(s.configDir(), "gateway.env")+" via the service wrapper only; pass the equivalent flags when running by hand)")
		return
	}
	s.Log.Info("setup: restarting the OpenShell gateway service")
	if err := s.ExecStream("brew", "services", "restart", "openshell"); err != nil {
		s.Log.Warn("brew services restart openshell failed; run it manually", "err", err)
		return
	}
	if waitFor(func() bool {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:17670", time.Second)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, 30*time.Second) {
		s.Log.Info("setup: gateway is listening", "address", "127.0.0.1:17670")
	} else {
		s.Log.Warn("setup: gateway did not start listening in time; check its logs",
			"log", "/opt/homebrew/var/log/openshell/openshell-gateway.err.log")
	}
}

func (s *Setup) pullImages(ctx context.Context, opts Options) {
	local := map[string]bool{}
	if images, err := s.RT.ImageList(ctx); err == nil {
		for _, img := range images {
			local[img.Reference] = true
		}
	}
	for _, ref := range []string{opts.DefaultImage, opts.SupervisorImage} {
		if ref == "" || local[ref] {
			continue
		}
		s.Log.Info("setup: pulling image (one-time)", "image", ref)
		if err := s.ExecStream("container", "image", "pull", ref, "--platform", "linux/arm64"); err != nil {
			s.Log.Warn("image pull failed; the driver will retry at first use", "image", ref, "err", err)
		}
	}
}

func upsertFileBlock(path string, lines []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	existing := ""
	if data, err := os.ReadFile(path); err == nil {
		existing = string(data)
	}
	return os.WriteFile(path, []byte(UpsertManagedBlock(existing, lines)), 0o600)
}

func waitFor(cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return cond()
}
