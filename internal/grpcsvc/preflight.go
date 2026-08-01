package grpcsvc

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/vyncint/openshell-driver-applecontainer/internal/backend"
	"github.com/vyncint/openshell-driver-applecontainer/internal/config"
)

// Preflight validates driver configuration at startup so misconfiguration
// surfaces immediately instead of at the first create (or worse, as a
// sandbox that silently never becomes Ready).
//
// Hard error: a loopback --grpc-endpoint — guest VMs can never reach the
// host's loopback, so every sandbox would hang in Provisioning.
// Warnings: empty endpoint, unreadable guest TLS files.
// Convenience: the configured vmnet network is created when absent.
func Preflight(ctx context.Context, cfg config.Config, rt backend.Runtime, log *slog.Logger) error {
	if cfg.GRPCEndpoint == "" {
		log.Warn("no --grpc-endpoint configured; sandbox creates will fail until it is set to the gateway address reachable from guest VMs")
	} else {
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

	networks, err := rt.NetworkList(ctx)
	if err != nil {
		log.Warn("could not verify the vmnet network (container runtime unreachable?); continuing",
			"network", cfg.Network, "err", err)
		return nil
	}
	if !slices.Contains(networks, cfg.Network) {
		if err := rt.NetworkCreate(ctx, cfg.Network); err != nil {
			log.Warn("failed to create the vmnet network; sandbox boots will fail until it exists",
				"network", cfg.Network, "err", err)
		} else {
			log.Info("created vmnet network", "network", cfg.Network)
		}
	}
	return nil
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
