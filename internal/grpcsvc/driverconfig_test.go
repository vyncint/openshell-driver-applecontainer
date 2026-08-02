package grpcsvc

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/vyncint/openshell-driver-applecontainer/internal/config"
)

func TestCheckDriverConfigPolicyMounts(t *testing.T) {
	volume := driverConfig{Mounts: []mountConfig{{Type: "volume", Source: "/Users/me/data", Target: "/data"}}}
	tmpfs := driverConfig{Mounts: []mountConfig{{Type: "tmpfs", Target: "/scratch"}}}

	// Default (mounts off): volume rejected, tmpfs allowed.
	base := config.Config{Network: "oshl"}
	if err := checkDriverConfigPolicy(base, volume); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Errorf("volume mount should be disabled by default, got %v", err)
	}
	if err := checkDriverConfigPolicy(base, tmpfs); err != nil {
		t.Errorf("tmpfs must always be allowed, got %v", err)
	}

	// Opt-in, no root: allowed anywhere.
	allow := config.Config{Network: "oshl", AllowHostMounts: true}
	if err := checkDriverConfigPolicy(allow, volume); err != nil {
		t.Errorf("volume mount should be allowed with --allow-host-mounts, got %v", err)
	}

	// Opt-in with a root: only sources under the root.
	rooted := config.Config{Network: "oshl", AllowHostMounts: true, HostMountRoot: "/Users/me"}
	if err := checkDriverConfigPolicy(rooted, volume); err != nil {
		t.Errorf("source under root should be allowed, got %v", err)
	}
	outside := driverConfig{Mounts: []mountConfig{{Type: "volume", Source: "/etc", Target: "/data"}}}
	if err := checkDriverConfigPolicy(rooted, outside); err == nil || !strings.Contains(err.Error(), "outside the permitted root") {
		t.Errorf("source outside root should be rejected, got %v", err)
	}
	// A sibling that only shares a prefix string is not "within".
	sibling := driverConfig{Mounts: []mountConfig{{Type: "volume", Source: "/Users/mensa", Target: "/data"}}}
	if err := checkDriverConfigPolicy(rooted, sibling); err == nil {
		t.Errorf("prefix-only sibling must be rejected")
	}
}

func TestCheckDriverConfigPolicyNetwork(t *testing.T) {
	cfg := config.Config{Network: "oshl", AllowedNetworks: []string{"lab"}}

	for _, ok := range []string{"", "oshl", "lab"} {
		if err := checkDriverConfigPolicy(cfg, driverConfig{Network: ok}); err != nil {
			t.Errorf("network %q should be allowed, got %v", ok, err)
		}
	}
	err := checkDriverConfigPolicy(cfg, driverConfig{Network: "prod"})
	if err == nil || !strings.Contains(err.Error(), "not permitted") {
		t.Errorf("unlisted network should be rejected, got %v", err)
	}
}

func mustStruct(t *testing.T, m map[string]any) *structpb.Struct {
	t.Helper()
	st, err := structpb.NewStruct(m)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestParseDriverConfigValid(t *testing.T) {
	st := mustStruct(t, map[string]any{
		"network": "custom-net",
		"kernel":  "/opt/kernels/landlock",
		"mounts": []any{
			map[string]any{"type": "volume", "source": "/Users/x/data", "target": "/data"},
			map[string]any{"type": "volume", "source": "/Users/x/rw", "target": "/rw", "read_only": false},
			map[string]any{"type": "tmpfs", "target": "/scratch"},
		},
	})
	cfg, err := parseDriverConfig(st)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Network != "custom-net" || cfg.Kernel != "/opt/kernels/landlock" {
		t.Errorf("cfg = %+v", cfg)
	}
	if len(cfg.Mounts) != 3 {
		t.Fatalf("mounts = %+v", cfg.Mounts)
	}
	if !cfg.Mounts[0].readOnly() {
		t.Error("volume mounts must default to read-only")
	}
	if cfg.Mounts[1].readOnly() {
		t.Error("explicit read_only=false ignored")
	}
}

func TestParseDriverConfigEmpty(t *testing.T) {
	if _, err := parseDriverConfig(nil); err != nil {
		t.Errorf("nil struct: %v", err)
	}
	if _, err := parseDriverConfig(mustStruct(t, map[string]any{})); err != nil {
		t.Errorf("empty struct: %v", err)
	}
}

func TestParseDriverConfigRejects(t *testing.T) {
	cases := []struct {
		name    string
		cfg     map[string]any
		wantSub string
	}{
		{"unknown key", map[string]any{"nope": 1}, "unknown"},
		{"reserved target /sandbox", map[string]any{"mounts": []any{
			map[string]any{"type": "volume", "source": "/tmp/x", "target": "/sandbox"}}}, "reserved"},
		{"under reserved /etc/openshell", map[string]any{"mounts": []any{
			map[string]any{"type": "volume", "source": "/tmp/x", "target": "/etc/openshell/policy.yaml"}}}, "reserved"},
		{"seed dir", map[string]any{"mounts": []any{
			map[string]any{"type": "volume", "source": "/tmp/x", "target": "/openshell-seed/tls"}}}, "reserved"},
		{"netns", map[string]any{"mounts": []any{
			map[string]any{"type": "tmpfs", "target": "/run/netns"}}}, "reserved"},
		{"supervisor dir", map[string]any{"mounts": []any{
			map[string]any{"type": "tmpfs", "target": "/opt/openshell/bin"}}}, "reserved"},
		{"root target", map[string]any{"mounts": []any{
			map[string]any{"type": "volume", "source": "/tmp/x", "target": "/"}}}, "root"},
		{"relative target", map[string]any{"mounts": []any{
			map[string]any{"type": "volume", "source": "/tmp/x", "target": "data"}}}, "absolute"},
		{"dirty target", map[string]any{"mounts": []any{
			map[string]any{"type": "volume", "source": "/tmp/x", "target": "/data/../etc"}}}, "normalized"},
		{"duplicate targets", map[string]any{"mounts": []any{
			map[string]any{"type": "tmpfs", "target": "/dup"},
			map[string]any{"type": "tmpfs", "target": "/dup"}}}, "duplicate"},
		{"relative volume source", map[string]any{"mounts": []any{
			map[string]any{"type": "volume", "source": "data", "target": "/data"}}}, "absolute host path"},
		{"tmpfs with source", map[string]any{"mounts": []any{
			map[string]any{"type": "tmpfs", "source": "/tmp/x", "target": "/scratch"}}}, "no source"},
		{"bind type unsupported", map[string]any{"mounts": []any{
			map[string]any{"type": "bind", "source": "/tmp/x", "target": "/data"}}}, "unsupported type"},
		{"relative kernel", map[string]any{"kernel": "kernels/foo"}, "absolute"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseDriverConfig(mustStruct(t, tc.cfg))
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("want error containing %q, got %v", tc.wantSub, err)
			}
		})
	}
}
