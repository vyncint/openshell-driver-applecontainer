package grpcsvc

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
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
			err := Preflight(context.Background(), &cfg, &backend.Fake{}, slog.Default())
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
			if err := Preflight(context.Background(), &cfg, &backend.Fake{}, slog.Default()); err == nil {
				t.Errorf("endpoint %q must fail preflight", ep)
			}
		})
	}
}

func TestPreflightDerivesEndpointFromNetwork(t *testing.T) {
	fake := &backend.Fake{}
	fake.AddNetwork(backend.Network{Name: "oshl", IPv4Gateway: "192.168.65.1", IPv4Subnet: "192.168.65.0/24"})
	cfg := testConfig()
	cfg.GRPCEndpoint = ""
	if err := Preflight(context.Background(), &cfg, fake, slog.Default()); err != nil {
		t.Fatal(err)
	}
	if cfg.GRPCEndpoint != "https://192.168.65.1:17670" {
		t.Errorf("derived endpoint = %q", cfg.GRPCEndpoint)
	}
}

func TestPreflightDerivesEndpointAfterCreatingNetwork(t *testing.T) {
	fake := &backend.Fake{} // no networks yet: preflight must create, then derive
	cfg := testConfig()
	cfg.GRPCEndpoint = ""
	if err := Preflight(context.Background(), &cfg, fake, slog.Default()); err != nil {
		t.Fatal(err)
	}
	if cfg.GRPCEndpoint != "https://192.168.65.1:17670" {
		t.Errorf("derived endpoint = %q", cfg.GRPCEndpoint)
	}
	nets, err := fake.Networks(context.Background())
	if err != nil || len(nets) != 1 || nets[0].Name != cfg.Network {
		t.Errorf("network not created: %v, %v", nets, err)
	}
}

func TestPreflightExplicitEndpointBeatsDerivation(t *testing.T) {
	fake := &backend.Fake{}
	fake.AddNetwork(backend.Network{Name: "oshl", IPv4Gateway: "192.168.65.1"})
	cfg := testConfig()
	cfg.GRPCEndpoint = "https://10.1.2.3:17670"
	if err := Preflight(context.Background(), &cfg, fake, slog.Default()); err != nil {
		t.Fatal(err)
	}
	if cfg.GRPCEndpoint != "https://10.1.2.3:17670" {
		t.Errorf("explicit endpoint overwritten: %q", cfg.GRPCEndpoint)
	}
}

func TestPreflightAcceptsRoutableEndpointAndEnsuresNetwork(t *testing.T) {
	fake := &backend.Fake{}
	cfg := testConfig()
	cfg.GRPCEndpoint = "https://192.168.65.1:17670"
	dir := t.TempDir()
	for _, name := range []string{"ca.crt", "tls.crt", "tls.key"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg.GuestTLSCA = filepath.Join(dir, "ca.crt")
	cfg.GuestTLSCert = filepath.Join(dir, "tls.crt")
	cfg.GuestTLSKey = filepath.Join(dir, "tls.key")

	countNetwork := func() int {
		nets, err := fake.Networks(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, net := range nets {
			if net.Name == cfg.Network {
				n++
			}
		}
		return n
	}

	if err := Preflight(context.Background(), &cfg, fake, slog.Default()); err != nil {
		t.Fatal(err)
	}
	if got := countNetwork(); got != 1 {
		t.Errorf("network %q auto-created %d times, want 1", cfg.Network, got)
	}

	// Second run: network exists, no duplicate creation.
	if err := Preflight(context.Background(), &cfg, fake, slog.Default()); err != nil {
		t.Fatal(err)
	}
	if got := countNetwork(); got != 1 {
		t.Errorf("network created %d times", got)
	}
}

func TestPreflightToleratesMissingTLS(t *testing.T) {
	cfg := testConfig()
	cfg.GRPCEndpoint = "https://192.168.65.1:17670"
	cfg.GuestTLSCA = "/nonexistent/ca.crt"
	cfg.GuestTLSCert = "/nonexistent/tls.crt"
	cfg.GuestTLSKey = "/nonexistent/tls.key"
	if err := Preflight(context.Background(), &cfg, &backend.Fake{}, slog.Default()); err != nil {
		t.Errorf("missing TLS files must warn, not fail: %v", err)
	}
}

func TestPreflightToleratesUnreachableRuntime(t *testing.T) {
	// Networks keeps failing even after the SystemStart attempt; preflight
	// must warn and continue with an empty endpoint (creates fail later
	// with FailedPrecondition), never error at startup.
	fake := &backend.Fake{NetworkListError: context.DeadlineExceeded}
	cfg := testConfig()
	cfg.GRPCEndpoint = ""
	if err := Preflight(context.Background(), &cfg, fake, slog.Default()); err != nil {
		t.Errorf("unreachable runtime must warn, not fail: %v", err)
	}
	if cfg.GRPCEndpoint != "" {
		t.Errorf("endpoint should stay empty when underivable, got %q", cfg.GRPCEndpoint)
	}
}
