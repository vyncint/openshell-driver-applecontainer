// Package config resolves driver configuration from flags and environment
// variables. Precedence: flag > environment > default. The gateway's own
// config file passes extension drivers nothing but the socket path, so this
// is the driver's entire configuration surface.
package config

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Env var prefix for every setting; e.g. --socket ↔ OSHL_AC_SOCKET.
const envPrefix = "OSHL_AC_"

// Config is the resolved driver configuration.
type Config struct {
	// Socket is the Unix socket path the gRPC server listens on. Keep it
	// short: macOS sun_path tops out around 104 bytes.
	Socket string
	// StateDir holds per-sandbox records and seed material.
	StateDir string
	// Network is the vmnet network sandbox VMs attach to.
	Network string
	// DefaultImage is advertised via GetCapabilities and used when a create
	// request has no image.
	DefaultImage string
	// SupervisorImage is the release-matched image the openshell-sandbox
	// binary is extracted from.
	SupervisorImage string
	// SupervisorImageExplicit records that the operator pinned
	// SupervisorImage themselves (flag or env). When false the tag is matched
	// to the installed gateway — see ResolveSupervisorImage.
	SupervisorImageExplicit bool
	// GRPCEndpoint is the gateway endpoint as reachable FROM INSIDE guest
	// VMs (e.g. https://192.168.65.1:17670). Injected as OPENSHELL_ENDPOINT.
	GRPCEndpoint string
	// GuestTLSCA/Cert/Key are host paths to the gateway's shared client TLS
	// triple, handed to every sandbox supervisor.
	GuestTLSCA   string
	GuestTLSCert string
	GuestTLSKey  string
	// Namespace is reported on observed sandboxes; the gateway leaves the
	// field to the driver.
	Namespace string
	// CPUs and MemoryMB size sandbox VMs when the request carries no
	// resource requirements.
	CPUs     int64
	MemoryMB int64
	// Kernel is a host kernel path applied to every sandbox VM by default
	// (container run --kernel) — e.g. a build with Landlock enabled. A
	// per-sandbox driver-config kernel overrides it.
	Kernel string
	// AllowHostMounts gates per-sandbox driver-config volume mounts (host
	// directory binds). Off by default: a sandbox spec cannot mount host
	// directories unless the operator opts in. tmpfs mounts are unaffected.
	AllowHostMounts bool
	// HostMountRoot, when set, constrains volume-mount sources to this
	// directory subtree (only meaningful with AllowHostMounts).
	HostMountRoot string
	// AllowedNetworks are the vmnet networks a per-sandbox driver-config
	// network override may select, in addition to Network. Empty means only
	// Network is permitted.
	AllowedNetworks []string
	// NetworkPolicyFile, when set, is a host path to a policy.yaml that
	// replaces the sandbox image's baked-in /etc/openshell/policy.yaml for
	// every sandbox this driver instance boots — e.g. to allowlist
	// additional egress a tool's self-updater needs. This is a driver-wide,
	// operator-only control: it is never settable per sandbox, since it
	// changes the guest's own security policy, not just this driver's.
	// Start from the image's shipped policy and add entries; replacing it
	// with something permissive weakens every sandbox's sandboxing.
	NetworkPolicyFile string
	// LogLevel is the driver's own log level (debug|info|warn|error) and
	// the default OPENSHELL_LOG_LEVEL for sandboxes without one.
	LogLevel string
}

func envOr(name, def string) string {
	if v, ok := os.LookupEnv(envPrefix + name); ok {
		return v
	}
	return def
}

func envOrInt(name string, def int64) int64 {
	if v, ok := os.LookupEnv(envPrefix + name); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

func envOrBool(name string, def bool) bool {
	if v, ok := os.LookupEnv(envPrefix + name); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

// defaultStateDir follows XDG state conventions like the upstream drivers.
func defaultStateDir() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "openshell-applecontainer")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "state"
	}
	return filepath.Join(home, ".local", "state", "openshell-applecontainer")
}

// SupervisorRepo is the registry repository the in-guest supervisor binary is
// extracted from. Its tag is the OpenShell release version: the supervisor runs
// inside every sandbox and speaks to the gateway, so a tag that lags the
// installed gateway is a silent protocol mismatch.
const SupervisorRepo = "ghcr.io/nvidia/openshell/supervisor"

// PinnedSupervisorTag is the OpenShell release this driver was developed
// against (see docs/CONTRACT.md). It is the fallback when the installed
// gateway's version cannot be determined.
const PinnedSupervisorTag = "0.0.96"

// PinnedSupervisorImage is the fallback supervisor image reference.
func PinnedSupervisorImage() string {
	return SupervisorRepo + ":" + PinnedSupervisorTag
}

// versionRe matches a bare semver-ish version as printed by --version output.
var versionRe = regexp.MustCompile(`^v?(\d+\.\d+\.\d+)$`)

// gatewayVersionProbe reads the installed gateway's version; swapped in tests.
var gatewayVersionProbe = func() string {
	out, err := exec.Command("openshell-gateway", "--version").Output()
	if err != nil {
		return ""
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return ""
	}
	m := versionRe.FindStringSubmatch(fields[len(fields)-1])
	if m == nil {
		return ""
	}
	return m[1]
}

// GatewayVersion returns the OpenShell gateway version installed on this host
// (e.g. "0.0.97"), or "" when it cannot be determined. The driver runs on the
// same host as the gateway, and the compute-driver contract carries no gateway
// version, so the binary itself is the only source.
func GatewayVersion() string { return gatewayVersionProbe() }

// ResolveSupervisorImage matches the supervisor image tag to the OpenShell
// gateway installed on this host. It is a no-op when the operator pinned
// --supervisor-image explicitly, and falls back to the pinned tag (with a
// warning) when the gateway version cannot be read.
//
// Shelling out makes this unsuitable for Parse, so callers invoke it right
// after parsing; forgetting to call it degrades to the pinned tag rather than
// to an empty image.
func (c *Config) ResolveSupervisorImage(log *slog.Logger) {
	if c.SupervisorImageExplicit {
		return
	}
	ver := GatewayVersion()
	if ver == "" {
		log.Warn("could not read the OpenShell gateway version; using the pinned supervisor image",
			"image", c.SupervisorImage,
			"hint", "pin it with --supervisor-image if the gateway is installed elsewhere")
		return
	}
	want := SupervisorRepo + ":" + ver
	if want == c.SupervisorImage {
		return
	}
	log.Info("matched the supervisor image to the installed gateway",
		"gateway_version", ver, "supervisor_image", want)
	c.SupervisorImage = want
}

// homebrewGatewayTLSDir is where the Homebrew openshell formula's
// post-install generates the gateway PKI.
const homebrewGatewayTLSDir = "/opt/homebrew/var/openshell/tls"

// defaultGuestTLSDir finds the gateway's TLS bundle: an explicit
// OPENSHELL_LOCAL_TLS_DIR wins, then the first existing of the XDG state
// location and the Homebrew install location, falling back to the XDG path
// (so warnings name where the bundle is expected).
func defaultGuestTLSDir() string {
	if v := os.Getenv("OPENSHELL_LOCAL_TLS_DIR"); v != "" {
		return v
	}
	xdg := ""
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		xdg = filepath.Join(v, "openshell", "tls")
	} else if home, err := os.UserHomeDir(); err == nil {
		xdg = filepath.Join(home, ".local", "state", "openshell", "tls")
	}
	for _, dir := range []string{xdg, homebrewGatewayTLSDir} {
		if dir == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "ca.crt")); err == nil {
			return dir
		}
	}
	if xdg != "" {
		return xdg
	}
	return homebrewGatewayTLSDir
}

// Parse resolves configuration from args (excluding argv[0]) and the
// environment.
func Parse(args []string) (Config, error) {
	tlsDir := defaultGuestTLSDir()
	var cfg Config
	fs := flag.NewFlagSet("openshell-driver-applecontainer", flag.ContinueOnError)
	fs.StringVar(&cfg.Socket, "socket", envOr("SOCKET", "/tmp/oshl-ac/driver.sock"), "unix socket path for the compute driver gRPC server")
	fs.StringVar(&cfg.StateDir, "state-dir", envOr("STATE_DIR", defaultStateDir()), "directory for sandbox records and seed material")
	fs.StringVar(&cfg.Network, "network", envOr("NETWORK", "oshl"), "vmnet network sandbox VMs attach to")
	fs.StringVar(&cfg.DefaultImage, "default-image", envOr("DEFAULT_IMAGE", "ghcr.io/nvidia/openshell-community/sandboxes/base:latest"), "default sandbox image")
	fs.StringVar(&cfg.SupervisorImage, "supervisor-image", envOr("SUPERVISOR_IMAGE", PinnedSupervisorImage()), "image the openshell-sandbox supervisor binary is extracted from (default: matched to the installed gateway's version)")
	fs.StringVar(&cfg.GRPCEndpoint, "grpc-endpoint", envOr("GRPC_ENDPOINT", ""), "gateway endpoint reachable from guest VMs; auto-derived from the vmnet network's gateway address when unset")
	fs.StringVar(&cfg.GuestTLSCA, "guest-tls-ca", envOr("GUEST_TLS_CA", filepath.Join(tlsDir, "ca.crt")), "gateway CA certificate handed to sandboxes")
	fs.StringVar(&cfg.GuestTLSCert, "guest-tls-cert", envOr("GUEST_TLS_CERT", filepath.Join(tlsDir, "client", "tls.crt")), "shared client certificate handed to sandboxes")
	fs.StringVar(&cfg.GuestTLSKey, "guest-tls-key", envOr("GUEST_TLS_KEY", filepath.Join(tlsDir, "client", "tls.key")), "shared client key handed to sandboxes")
	fs.StringVar(&cfg.Namespace, "namespace", envOr("NAMESPACE", "default"), "namespace reported for sandboxes")
	fs.Int64Var(&cfg.CPUs, "cpus", envOrInt("CPUS", 2), "default vCPUs per sandbox VM")
	fs.Int64Var(&cfg.MemoryMB, "memory", envOrInt("MEMORY_MB", 2048), "default memory per sandbox VM in MiB")
	fs.StringVar(&cfg.Kernel, "kernel", envOr("KERNEL", ""), "host kernel path used for every sandbox VM by default (e.g. a Landlock-enabled build); per-sandbox driver config overrides it")
	fs.BoolVar(&cfg.AllowHostMounts, "allow-host-mounts", envOrBool("ALLOW_HOST_MOUNTS", false), "permit per-sandbox driver-config volume mounts of host directories (off by default)")
	fs.StringVar(&cfg.HostMountRoot, "host-mount-root", envOr("HOST_MOUNT_ROOT", ""), "when set, volume-mount sources must live under this directory")
	allowedNetworks := fs.String("allowed-networks", envOr("ALLOWED_NETWORKS", ""), "comma-separated vmnet networks a sandbox may select via driver config, in addition to --network")
	fs.StringVar(&cfg.NetworkPolicyFile, "network-policy-file", envOr("NETWORK_POLICY_FILE", ""), "host path to a policy.yaml that replaces the sandbox image's network policy for every sandbox (operator-only; see SECURITY.md)")
	fs.StringVar(&cfg.LogLevel, "log-level", envOr("LOG_LEVEL", "info"), "log level: debug|info|warn|error")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	cfg.AllowedNetworks = splitList(*allowedNetworks)
	// Remember whether the operator pinned the supervisor image themselves; if
	// they did, ResolveSupervisorImage leaves it alone.
	if _, ok := os.LookupEnv(envPrefix + "SUPERVISOR_IMAGE"); ok {
		cfg.SupervisorImageExplicit = true
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "supervisor-image" {
			cfg.SupervisorImageExplicit = true
		}
	})
	if cfg.Socket == "" {
		return Config{}, fmt.Errorf("config: --socket must not be empty")
	}
	if cfg.StateDir == "" {
		return Config{}, fmt.Errorf("config: --state-dir must not be empty")
	}
	if cfg.HostMountRoot != "" && !filepath.IsAbs(cfg.HostMountRoot) {
		return Config{}, fmt.Errorf("config: --host-mount-root must be an absolute path")
	}
	if cfg.NetworkPolicyFile != "" && !filepath.IsAbs(cfg.NetworkPolicyFile) {
		return Config{}, fmt.Errorf("config: --network-policy-file must be an absolute path")
	}
	return cfg, nil
}

// splitList parses a comma-separated flag value into a trimmed, non-empty
// slice.
func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
