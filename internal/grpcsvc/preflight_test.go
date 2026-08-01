package grpcsvc

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vyncint/openshell-driver-applecontainer/internal/backend"
)

func TestPreflightRejectsLoopbackEndpoint(t *testing.T) {
	for _, ep := range []string{
		"https://127.0.0.1:17670",
		"https://localhost:17670",
		"https://[::1]:17670",
		"http://127.0.0.9:17670", // whole 127/8 block is loopback
	} {
		t.Run(ep, func(t *testing.T) {
			cfg := testConfig()
			cfg.GRPCEndpoint = ep
			err := Preflight(context.Background(), cfg, &backend.Fake{}, slog.Default())
			if err == nil || !strings.Contains(err.Error(), "loopback") {
				t.Errorf("want loopback error, got %v", err)
			}
		})
	}
}

func TestPreflightRejectsInvalidEndpoint(t *testing.T) {
	for _, ep := range []string{
		"not a url",
		"HTTPS://192.168.65.1:17670", // uppercase scheme: TLS env injection matches lowercase https:// only
		"tcp://192.168.65.1:17670",   // non-http scheme
		"192.168.65.1:17670",         // no scheme
	} {
		t.Run(ep, func(t *testing.T) {
			cfg := testConfig()
			cfg.GRPCEndpoint = ep
			if err := Preflight(context.Background(), cfg, &backend.Fake{}, slog.Default()); err == nil {
				t.Errorf("endpoint %q must fail preflight", ep)
			}
		})
	}
}

func TestPreflightAcceptsRoutableEndpointAndEnsuresNetwork(t *testing.T) {
	fake := &backend.Fake{}
	cfg := testConfig()
	cfg.GRPCEndpoint = "https://192.168.65.1:17670"
	// TLS files exist for this case.
	dir := t.TempDir()
	for _, name := range []string{"ca.crt", "tls.crt", "tls.key"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg.GuestTLSCA = filepath.Join(dir, "ca.crt")
	cfg.GuestTLSCert = filepath.Join(dir, "tls.crt")
	cfg.GuestTLSKey = filepath.Join(dir, "tls.key")

	if err := Preflight(context.Background(), cfg, fake, slog.Default()); err != nil {
		t.Fatal(err)
	}
	nets, err := fake.NetworkList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(nets, cfg.Network) {
		t.Errorf("network %q not auto-created: %v", cfg.Network, nets)
	}

	// Second run: network exists, no duplicate creation.
	if err := Preflight(context.Background(), cfg, fake, slog.Default()); err != nil {
		t.Fatal(err)
	}
	nets, _ = fake.NetworkList(context.Background())
	count := 0
	for _, n := range nets {
		if n == cfg.Network {
			count++
		}
	}
	if count != 1 {
		t.Errorf("network created %d times", count)
	}
}

func TestPreflightToleratesEmptyEndpointAndMissingTLS(t *testing.T) {
	cfg := testConfig()
	cfg.GRPCEndpoint = "" // warn only
	if err := Preflight(context.Background(), cfg, &backend.Fake{}, slog.Default()); err != nil {
		t.Errorf("empty endpoint must not fail preflight: %v", err)
	}

	cfg.GRPCEndpoint = "https://192.168.65.1:17670"
	cfg.GuestTLSCA = "/nonexistent/ca.crt"
	cfg.GuestTLSCert = "/nonexistent/tls.crt"
	cfg.GuestTLSKey = "/nonexistent/tls.key"
	if err := Preflight(context.Background(), cfg, &backend.Fake{}, slog.Default()); err != nil {
		t.Errorf("missing TLS files must warn, not fail: %v", err)
	}
}

func TestPreflightToleratesUnreachableRuntime(t *testing.T) {
	fake := &backend.Fake{NetworkListError: context.DeadlineExceeded}
	cfg := testConfig()
	cfg.GRPCEndpoint = "http://192.168.65.1:17670"
	if err := Preflight(context.Background(), cfg, fake, slog.Default()); err != nil {
		t.Errorf("unreachable runtime must warn, not fail: %v", err)
	}
}
