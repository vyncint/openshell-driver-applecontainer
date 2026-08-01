// Package config resolves driver configuration from flags and environment
// variables. Precedence: flag > environment > default. The gateway's own
// config file passes extension drivers nothing but the socket path, so this
// is the driver's entire configuration surface.
package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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

// defaultGuestTLSDir mirrors the gateway's local TLS state location
// ($XDG_STATE_HOME/openshell/tls) where generate-certs writes the bundle.
func defaultGuestTLSDir() string {
	if v := os.Getenv("OPENSHELL_LOCAL_TLS_DIR"); v != "" {
		return v
	}
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "openshell", "tls")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "state", "openshell", "tls")
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
	fs.StringVar(&cfg.SupervisorImage, "supervisor-image", envOr("SUPERVISOR_IMAGE", "ghcr.io/nvidia/openshell/supervisor:0.0.96"), "image the openshell-sandbox supervisor binary is extracted from")
	fs.StringVar(&cfg.GRPCEndpoint, "grpc-endpoint", envOr("GRPC_ENDPOINT", ""), "gateway endpoint reachable from guest VMs (e.g. https://192.168.65.1:17670)")
	fs.StringVar(&cfg.GuestTLSCA, "guest-tls-ca", envOr("GUEST_TLS_CA", filepath.Join(tlsDir, "ca.crt")), "gateway CA certificate handed to sandboxes")
	fs.StringVar(&cfg.GuestTLSCert, "guest-tls-cert", envOr("GUEST_TLS_CERT", filepath.Join(tlsDir, "client", "tls.crt")), "shared client certificate handed to sandboxes")
	fs.StringVar(&cfg.GuestTLSKey, "guest-tls-key", envOr("GUEST_TLS_KEY", filepath.Join(tlsDir, "client", "tls.key")), "shared client key handed to sandboxes")
	fs.StringVar(&cfg.Namespace, "namespace", envOr("NAMESPACE", "default"), "namespace reported for sandboxes")
	fs.Int64Var(&cfg.CPUs, "cpus", envOrInt("CPUS", 2), "default vCPUs per sandbox VM")
	fs.Int64Var(&cfg.MemoryMB, "memory", envOrInt("MEMORY_MB", 2048), "default memory per sandbox VM in MiB")
	fs.StringVar(&cfg.LogLevel, "log-level", envOr("LOG_LEVEL", "info"), "log level: debug|info|warn|error")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if cfg.Socket == "" {
		return Config{}, fmt.Errorf("config: --socket must not be empty")
	}
	if cfg.StateDir == "" {
		return Config{}, fmt.Errorf("config: --state-dir must not be empty")
	}
	return cfg, nil
}
