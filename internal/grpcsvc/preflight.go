package grpcsvc

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/vyncint/openshell-driver-applecontainer/internal/backend"
	"github.com/vyncint/openshell-driver-applecontainer/internal/config"
)

// gatewayPort is OpenShell's default server port, used when deriving the
// guest-reachable endpoint from the vmnet network.
const gatewayPort = "17670"

// Preflight validates and completes driver configuration at startup so
// misconfiguration surfaces immediately instead of at the first create (or
// worse, as a sandbox that silently never becomes Ready).
//
//   - The configured vmnet network is created when absent (starting the
//     container runtime first when it is down).
//   - An empty --grpc-endpoint is derived from the network's host-side
//     gateway address (https://<gateway-ip>:17670) — the zero-config path.
//   - A loopback endpoint is a hard error: guest VMs can never reach the
//     host's loopback, so every sandbox would hang in Provisioning.
//   - Unreadable guest TLS files warn.
func Preflight(ctx context.Context, cfg *config.Config, rt backend.Runtime, log *slog.Logger) error {
	gatewayIP := ensureNetwork(ctx, cfg.Network, rt, log)

	if cfg.GRPCEndpoint == "" {
		if gatewayIP != "" {
			cfg.GRPCEndpoint = "https://" + net.JoinHostPort(gatewayIP, gatewayPort)
			log.Info("derived gateway endpoint from the vmnet network",
				"network", cfg.Network, "endpoint", cfg.GRPCEndpoint)
		} else {
			log.Warn("no --grpc-endpoint configured and none could be derived; sandbox creates will fail until it is set to the gateway address reachable from guest VMs")
		}
	}

	if cfg.GRPCEndpoint != "" {
		// The rest of the driver (and the in-guest supervisor) switches TLS
		// behavior on the literal lowercase "https://" prefix, so anything
		// else — other schemes, uppercase variants — would pass here and
		// then misbehave at provisioning. Require the canonical form.
		if !strings.HasPrefix(cfg.GRPCEndpoint, "http://") && !strings.HasPrefix(cfg.GRPCEndpoint, "https://") {
			return fmt.Errorf("preflight: --grpc-endpoint %q must be an http:// or https:// URL (lowercase scheme)", cfg.GRPCEndpoint)
		}
		u, err := url.Parse(cfg.GRPCEndpoint)
		if err != nil || u.Hostname() == "" {
			return fmt.Errorf("preflight: --grpc-endpoint %q is not a valid URL", cfg.GRPCEndpoint)
		}
		if isLoopbackHost(u.Hostname()) {
			return fmt.Errorf("preflight: --grpc-endpoint %q points at loopback, which guest VMs can never reach; "+
				"use the vmnet gateway address (e.g. https://192.168.65.1:17670), start the gateway with a non-loopback "+
				"bind address, and make sure its server certificate carries a SAN for that address", cfg.GRPCEndpoint)
		}
		if strings.HasPrefix(cfg.GRPCEndpoint, "https://") {
			for what, p := range map[string]string{
				"guest TLS CA":   cfg.GuestTLSCA,
				"guest TLS cert": cfg.GuestTLSCert,
				"guest TLS key":  cfg.GuestTLSKey,
			} {
				if _, err := os.Stat(p); err != nil {
					log.Warn("guest TLS material is not readable; sandbox provisioning will fail until it is",
						"file", what, "path", p, "err", err)
				}
			}
		}
	}
	return nil
}

// ensureNetwork makes sure the vmnet network exists and returns its
// host-side gateway address, or "" when the runtime is unavailable. A down
// container runtime gets one start attempt (the state after a reboot).
func ensureNetwork(ctx context.Context, name string, rt backend.Runtime, log *slog.Logger) string {
	networks, err := rt.Networks(ctx)
	if err != nil {
		log.Warn("container runtime unreachable; attempting to start it", "err", err)
		if serr := rt.SystemStart(ctx); serr != nil {
			log.Warn("could not start the container runtime; continuing without network checks", "err", serr)
			return ""
		}
		if networks, err = rt.Networks(ctx); err != nil {
			log.Warn("could not list vmnet networks; continuing without network checks", "err", err)
			return ""
		}
	}
	for _, n := range networks {
		if n.Name == name {
			return n.IPv4Gateway
		}
	}
	if err := rt.NetworkCreate(ctx, name); err != nil {
		log.Warn("failed to create the vmnet network; sandbox boots will fail until it exists",
			"network", name, "err", err)
		return ""
	}
	log.Info("created vmnet network", "network", name)
	networks, err = rt.Networks(ctx)
	if err != nil {
		return ""
	}
	for _, n := range networks {
		if n.Name == name {
			return n.IPv4Gateway
		}
	}
	return ""
}

// isLoopbackHost reports whether an endpoint hostname can only ever resolve
// to the host's own loopback.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
