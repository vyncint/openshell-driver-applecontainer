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
	"regexp"
	"runtime"
	"strconv"
	"strings"
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
	// DriverVersion is reported in the version summary; "" omits it.
	DriverVersion string
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
	// HasOpenShell probes whether the OpenShell Homebrew package is present.
	// nil uses the real check; tests inject a stub.
	HasOpenShell func() bool
	// ACUninstaller is apple/container's own uninstaller script, run under
	// `cleanup --all`. OpenShellVarDir is OpenShell's Homebrew var directory,
	// removed under `cleanup --all -d`. Both default to their real host paths
	// in New(); tests point them at temp dirs (or "" to skip).
	ACUninstaller   string
	OpenShellVarDir string
	// readyTimeout bounds how long to wait for the driver socket after a
	// (re)start; overridden in tests.
	readyTimeout time.Duration
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
		RT:              rt,
		Log:             log,
		Home:            home,
		UID:             os.Getuid(),
		BinPath:         bin,
		ACUninstaller:   "/usr/local/bin/uninstall-container.sh",
		OpenShellVarDir: "/opt/homebrew/var/openshell",
		readyTimeout:    15 * time.Second,
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

	// 1b. Default guest kernel. apple/container cannot boot any VM without
	// one, and a fresh install (or one whose user data was deleted, e.g. by
	// `cleanup --all -d`) ships without it — every sandbox create would fail
	// with "default kernel not configured for architecture".
	s.ensureKernel()

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
	// on exit). installAgent confirms the service actually started, with a
	// forced-restart retry, so a failure here is real.
	if err := s.installAgent(tlsDir, opts.Socket); err != nil {
		return err
	}
	step("driver service running", "socket", opts.Socket)

	// 7. Restart the gateway service so it dials the driver.
	s.restartGatewayService()

	// 8. Optionally pre-pull the images so the first sandbox is fast.
	if opts.PullImages {
		s.pullImages(ctx, opts)
	} else {
		step("skipping image pre-pull; the first sandbox create will pull " + opts.DefaultImage)
	}

	// 9. Report the resolved component versions so a mismatch is visible.
	s.logVersions(opts)

	fmt.Printf("\nSetup complete. Try:\n\n    openshell sandbox create --name demo\n    openshell sandbox exec -n demo -- uname -a\n    openshell sandbox delete demo\n\nRe-run `%s setup` any time to repair the installation.\n", filepath.Base(s.BinPath))
	return nil
}

// logVersions prints the driver / gateway / apple-container versions actually
// installed, and warns when the supervisor image tag does not match the
// gateway. The supervisor runs inside every sandbox and speaks to the gateway,
// so a lagging tag is a protocol mismatch that otherwise fails silently.
func (s *Setup) logVersions(opts Options) {
	gwVer := probeVersion(s.Exec, "openshell-gateway")
	acVer := probeVersion(s.Exec, "container")
	s.Log.Info("setup: component versions",
		"driver", orUnknown(opts.DriverVersion),
		"openshell_gateway", orUnknown(gwVer),
		"apple_container", orUnknown(acVer),
		"supervisor_image", opts.SupervisorImage)

	if gwVer == "" || opts.SupervisorImage == "" {
		return
	}
	if tag := opts.SupervisorImage[strings.LastIndex(opts.SupervisorImage, ":")+1:]; tag != gwVer {
		s.Log.Warn("setup: supervisor image tag does not match the installed gateway; sandboxes may fail to connect",
			"gateway", gwVer, "supervisor_tag", tag,
			"hint", "unset --supervisor-image/OSHL_AC_SUPERVISOR_IMAGE to match it automatically")
	}
}

// probeVersion runs `<bin> --version` and returns the trailing semver-ish
// token, or "" when the binary is absent or prints something unexpected.
func probeVersion(run func(string, ...string) (string, error), bin string) string {
	out, err := run(bin, "--version")
	if err != nil {
		return ""
	}
	for _, f := range strings.Fields(out) {
		if m := semverRe.FindStringSubmatch(strings.Trim(f, "()")); m != nil {
			return m[1]
		}
	}
	return ""
}

var semverRe = regexp.MustCompile(`^v?(\d+\.\d+\.\d+)$`)

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// CleanupOptions configures a cleanup run. The zero value reproduces the
// historical `uninstall`: remove the driver service and its gateway wiring
// but keep all data and prerequisites.
type CleanupOptions struct {
	// DeleteData also removes the driver's own data — its state directory,
	// socket directory, the vmnet network, and the pulled sandbox/supervisor
	// images. This is the "-d" (vs "-k") distinction from apple/container's
	// uninstaller.
	DeleteData bool
	// All also removes the prerequisites the driver sits on: the OpenShell
	// Homebrew package and apple/container (via its own installed
	// uninstaller). Off by default — a plain cleanup only touches the driver.
	All bool

	// The following are resolved from config and consulted only when
	// DeleteData is set.
	Network         string
	Socket          string
	StateDir        string
	DefaultImage    string
	SupervisorImage string
}

// Cleanup reverses setup. With the zero-value options it removes the driver
// launchd service and its gateway.env wiring and stops the gateway, leaving
// data and prerequisites untouched (the historical `uninstall`). DeleteData
// additionally removes the driver's own data; All additionally removes
// OpenShell and apple/container.
func (s *Setup) Cleanup(opts CleanupOptions) error {
	// 1. Driver launchd service.
	target := fmt.Sprintf("gui/%d/%s", s.UID, AgentLabel)
	if out, err := s.Exec("launchctl", "bootout", target); err != nil {
		s.Log.Debug("launchctl bootout", "out", out, "err", err)
	}
	if err := os.Remove(s.agentPlistPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	s.Log.Info("cleanup: driver service removed")

	// 2. gateway.env managed block (unmanaged lines are left in place).
	envPath := filepath.Join(s.configDir(), "gateway.env")
	if data, err := os.ReadFile(envPath); err == nil {
		rest := RemoveManagedBlock(string(data))
		if len(rest) == 0 || rest == "\n" {
			_ = os.Remove(envPath)
		} else if err := os.WriteFile(envPath, []byte(rest), 0o600); err != nil {
			return err
		}
		s.Log.Info("cleanup: gateway service configuration removed", "file", envPath)
	}

	// 3. Stop the gateway — with the driver gone it has no compute backend.
	// (When All uninstalls OpenShell below, `brew uninstall` stops it too;
	// stopping here first keeps a plain cleanup tidy.)
	if s.openShellInstalled() {
		s.Log.Info("cleanup: stopping the gateway service")
		if err := s.ExecStream("brew", "services", "stop", "openshell"); err != nil {
			s.Log.Warn("brew services stop openshell failed", "err", err)
		}
	}

	// 4. Driver-owned data.
	if opts.DeleteData {
		s.deleteDriverData(opts)
	}

	// 5. Prerequisites.
	if opts.All {
		s.removePrerequisites(opts)
	}

	switch {
	case opts.DeleteData && opts.All:
		s.Log.Info("cleanup: done (full teardown — driver, its data, OpenShell and apple/container)")
	case opts.DeleteData:
		s.Log.Info("cleanup: done (driver and its data removed; OpenShell and apple/container kept)")
	case opts.All:
		s.Log.Info("cleanup: done (driver, OpenShell and apple/container removed; driver data kept)")
	default:
		s.Log.Info("cleanup: done (driver removed; data, network, images and prerequisites kept)")
	}
	return nil
}

// deleteDriverData removes the driver's own state, socket directory, pulled
// images and vmnet network. Best-effort: a missing target is not an error.
func (s *Setup) deleteDriverData(opts CleanupOptions) {
	if opts.StateDir != "" {
		if err := os.RemoveAll(opts.StateDir); err != nil {
			s.Log.Warn("cleanup: remove state dir", "dir", opts.StateDir, "err", err)
		} else {
			s.Log.Info("cleanup: removed driver state", "dir", opts.StateDir)
		}
	}
	if opts.Socket != "" {
		sockDir := filepath.Dir(opts.Socket)
		if err := os.RemoveAll(sockDir); err != nil {
			s.Log.Warn("cleanup: remove socket dir", "dir", sockDir, "err", err)
		}
	}
	for _, ref := range []string{opts.DefaultImage, opts.SupervisorImage} {
		if ref == "" {
			continue
		}
		if out, err := s.Exec("container", "image", "rm", ref); err != nil {
			s.Log.Debug("cleanup: remove image (may not be present)", "image", ref, "out", out, "err", err)
		} else {
			s.Log.Info("cleanup: removed image", "image", ref)
		}
	}
	if opts.Network != "" {
		if out, err := s.Exec("container", "network", "rm", opts.Network); err != nil {
			s.Log.Debug("cleanup: remove network (may not be present)", "network", opts.Network, "out", out, "err", err)
		} else {
			s.Log.Info("cleanup: removed vmnet network", "network", opts.Network)
		}
	}
}

// removePrerequisites uninstalls OpenShell (Homebrew) and apple/container (via
// its own installed uninstaller). apple/container's uninstaller needs sudo, so
// it runs attached to the terminal to prompt for a password.
func (s *Setup) removePrerequisites(opts CleanupOptions) {
	if s.openShellInstalled() {
		s.Log.Info("cleanup: uninstalling OpenShell (brew)")
		if err := s.ExecStream("brew", "uninstall", "openshell"); err != nil {
			s.Log.Warn("brew uninstall openshell failed", "err", err)
		}
	}
	if opts.DeleteData {
		// brew uninstall leaves OpenShell's user config (CLI gateway
		// registrations) and its var dir (TLS/logs) behind.
		dirs := []string{s.configDir()}
		if s.OpenShellVarDir != "" {
			dirs = append(dirs, s.OpenShellVarDir)
		}
		for _, dir := range dirs {
			if err := os.RemoveAll(dir); err != nil {
				s.Log.Warn("cleanup: remove OpenShell data", "dir", dir, "err", err)
			}
		}
	}
	if s.ACUninstaller == "" {
		return
	}
	if _, err := os.Stat(s.ACUninstaller); err != nil {
		s.Log.Info("cleanup: apple/container uninstaller not found; skipping it", "path", s.ACUninstaller)
		return
	}
	// The uninstaller refuses to run while the runtime service is up.
	if err := s.ExecStream("container", "system", "stop"); err != nil {
		s.Log.Debug("container system stop", "err", err)
	}
	dataFlag := "-k"
	if opts.DeleteData {
		dataFlag = "-d"
	}
	s.Log.Info("cleanup: uninstalling apple/container (its uninstaller needs sudo)", "data", dataFlag)
	if err := s.ExecStream(s.ACUninstaller, dataFlag); err != nil {
		s.Log.Warn("apple/container uninstaller failed", "err", err)
	}
}

// defaultKernelPath is where apple/container records the default guest kernel
// for this architecture (a symlink into its kernels directory).
func (s *Setup) defaultKernelPath() string {
	return filepath.Join(s.Home, "Library", "Application Support", "com.apple.container",
		"kernels", "default.kernel-"+runtime.GOARCH)
}

// ensureKernel installs apple/container's recommended guest kernel when no
// default is configured. Without it every sandbox create fails at image
// unpack with "default kernel not configured for architecture".
//
// This is deliberately non-fatal: setup is the documented repair command, so a
// transient download failure must not stop it from fixing the rest of the
// wiring. It warns with the exact manual fallback instead.
func (s *Setup) ensureKernel() {
	if _, err := os.Lstat(s.defaultKernelPath()); err == nil {
		return
	}
	s.Log.Info("setup: no default guest kernel configured; installing the recommended one (one-time, ~600 MB)")
	err := s.ExecStream("container", "system", "kernel", "set", "--recommended")
	if err == nil {
		s.Log.Info("setup: default guest kernel installed")
		return
	}
	s.Log.Warn("setup: could not install the recommended guest kernel — SANDBOXES WILL NOT BOOT until one is set",
		"err", err,
		"retry", "container system kernel set --recommended",
		"fallback", "download the kata-static arm64 tarball with curl, then: container system kernel set --arch arm64 --tar <file> --binary opt/kata/share/kata-containers/vmlinux-<version>")
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
	// The gateway address may not be assigned the instant create returns on
	// a fresh machine; poll briefly for it.
	var findErr error
	waitFor(func() bool { ip, findErr = find(); return findErr == nil && ip != "" }, 10*time.Second)
	if findErr != nil || ip == "" {
		return "", fmt.Errorf("vmnet network %s has no gateway address after creation (%v)", name, findErr)
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
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return err
	}
	plistPath := s.agentPlistPath()
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o700); err != nil {
		return err
	}
	plist := RenderLaunchAgent(s.BinPath, tlsDir, logPath)
	if err := os.WriteFile(plistPath, []byte(plist), 0o600); err != nil {
		return err
	}
	target := fmt.Sprintf("gui/%d/%s", s.UID, AgentLabel)
	socketUp := func() bool { _, err := os.Stat(socket); return err == nil }
	// serviceLoaded reports whether launchd still has the label registered.
	// This is stricter than "socket exists": a graceful drain removes the
	// socket up to ~10s before the process exits and launchd unloads it,
	// and bootstrapping over a still-registered label fails with EIO.
	serviceLoaded := func() bool { _, err := s.Exec("launchctl", "print", target); return err == nil }

	if out, err := s.Exec("launchctl", "bootout", target); err != nil {
		s.Log.Debug("launchctl bootout (fresh install is fine)", "out", out, "err", err)
	}
	waitFor(func() bool { return !serviceLoaded() }, s.readyTimeout)

	// Bootstrap, retrying while the outgoing instance finishes unloading
	// (the transient EIO seen right after a binary upgrade).
	var bootErr error
	for attempt := 0; attempt < 3; attempt++ {
		out, err := s.Exec("launchctl", "bootstrap", "gui/"+strconv.Itoa(s.UID), plistPath)
		if err == nil {
			bootErr = nil
			break
		}
		bootErr = fmt.Errorf("launchctl bootstrap failed: %v: %s", err, strings.TrimSpace(out))
		waitFor(func() bool { return !serviceLoaded() }, s.readyTimeout)
	}
	if bootErr != nil {
		return bootErr
	}

	// Confirm the driver actually started; a bounce can still leave it
	// loaded-but-not-running. Force a restart and retry once before failing.
	if !waitFor(socketUp, s.readyTimeout) {
		s.Log.Warn("setup: driver service did not come up after bootstrap; forcing a restart")
		if out, err := s.Exec("launchctl", "kickstart", "-k", target); err != nil {
			s.Log.Debug("launchctl kickstart", "out", out, "err", err)
		}
		if !waitFor(socketUp, s.readyTimeout) {
			return fmt.Errorf("driver service did not start; check the log at %s", logPath)
		}
	}
	s.Log.Info("setup: driver installed as a launchd service", "plist", plistPath, "binary", s.BinPath, "log", logPath)
	return nil
}

// brewHasOpenShell reports whether the OpenShell Homebrew package is installed.
func brewHasOpenShell() bool {
	if _, err := exec.LookPath("brew"); err != nil {
		return false
	}
	_, err := os.Stat("/opt/homebrew/opt/openshell")
	return err == nil
}

// openShellInstalled is the probe used throughout setup/cleanup; it honors an
// injected HasOpenShell stub and otherwise runs the real check.
func (s *Setup) openShellInstalled() bool {
	if s.HasOpenShell != nil {
		return s.HasOpenShell()
	}
	return brewHasOpenShell()
}

func (s *Setup) restartGatewayService() {
	if !s.openShellInstalled() {
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
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
