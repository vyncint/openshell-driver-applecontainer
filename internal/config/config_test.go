package config

import (
	"testing"
)

func TestDefaults(t *testing.T) {
	cfg, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Socket != "/tmp/oshl-ac/driver.sock" {
		t.Errorf("socket = %q", cfg.Socket)
	}
	if cfg.Network != "oshl" {
		t.Errorf("network = %q", cfg.Network)
	}
	if cfg.DefaultImage != "ghcr.io/nvidia/openshell-community/sandboxes/base:latest" {
		t.Errorf("default image = %q", cfg.DefaultImage)
	}
	// Parse yields the pinned fallback; ResolveSupervisorImage later matches it
	// to the installed gateway (see supervisor_test.go).
	if cfg.SupervisorImage != PinnedSupervisorImage() {
		t.Errorf("supervisor image = %q", cfg.SupervisorImage)
	}
	if cfg.CPUs != 2 || cfg.MemoryMB != 2048 {
		t.Errorf("resources = %d cpu / %d MB", cfg.CPUs, cfg.MemoryMB)
	}
	if cfg.Namespace != "default" {
		t.Errorf("namespace = %q", cfg.Namespace)
	}
	if cfg.Kernel != "" {
		t.Errorf("kernel default = %q, want empty (runtime default kernel)", cfg.Kernel)
	}
	if cfg.AllowHostMounts {
		t.Error("host mounts must be off by default")
	}
	if cfg.HostMountRoot != "" || len(cfg.AllowedNetworks) != 0 {
		t.Errorf("mount root / allowed networks should default empty: %q %v", cfg.HostMountRoot, cfg.AllowedNetworks)
	}
}

func TestSecurityFlags(t *testing.T) {
	cfg, err := Parse([]string{
		"--allow-host-mounts",
		"--host-mount-root", "/srv/mounts",
		"--allowed-networks", "lab, prod ,,",
		"--network-policy-file", "/etc/oshl-ac/policy.yaml",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AllowHostMounts {
		t.Error("--allow-host-mounts not parsed")
	}
	if cfg.HostMountRoot != "/srv/mounts" {
		t.Errorf("host mount root = %q", cfg.HostMountRoot)
	}
	if len(cfg.AllowedNetworks) != 2 || cfg.AllowedNetworks[0] != "lab" || cfg.AllowedNetworks[1] != "prod" {
		t.Errorf("allowed networks = %v (blanks should be dropped)", cfg.AllowedNetworks)
	}
	if cfg.NetworkPolicyFile != "/etc/oshl-ac/policy.yaml" {
		t.Errorf("network policy file = %q", cfg.NetworkPolicyFile)
	}

	// Env fallback for the bool.
	t.Setenv("OSHL_AC_ALLOW_HOST_MOUNTS", "true")
	cfg, err = Parse(nil)
	if err != nil || !cfg.AllowHostMounts {
		t.Errorf("env bool not honored: %v %v", cfg.AllowHostMounts, err)
	}

	// A relative mount root is rejected.
	if _, err := Parse([]string{"--host-mount-root", "relative/path"}); err == nil {
		t.Error("relative --host-mount-root should be rejected")
	}

	// A relative policy file is rejected.
	if _, err := Parse([]string{"--network-policy-file", "relative/policy.yaml"}); err == nil {
		t.Error("relative --network-policy-file should be rejected")
	}

	// Unset by default.
	if def, _ := Parse(nil); def.NetworkPolicyFile != "" {
		t.Errorf("network policy file default = %q, want empty", def.NetworkPolicyFile)
	}
}

func TestKernelFlagAndEnv(t *testing.T) {
	t.Setenv("OSHL_AC_KERNEL", "/from/env")
	cfg, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Kernel != "/from/env" {
		t.Errorf("kernel = %q, want env value", cfg.Kernel)
	}
	cfg, err = Parse([]string{"--kernel", "/from/flag"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Kernel != "/from/flag" {
		t.Errorf("kernel = %q, want flag value", cfg.Kernel)
	}
}

func TestFlagBeatsEnv(t *testing.T) {
	t.Setenv("OSHL_AC_NETWORK", "from-env")
	t.Setenv("OSHL_AC_CPUS", "8")

	cfg, err := Parse([]string{"--network", "from-flag"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Network != "from-flag" {
		t.Errorf("network = %q, want flag value", cfg.Network)
	}
	if cfg.CPUs != 8 {
		t.Errorf("cpus = %d, want env value 8", cfg.CPUs)
	}
}

func TestEmptySocketRejected(t *testing.T) {
	if _, err := Parse([]string{"--socket", ""}); err == nil {
		t.Error("empty socket accepted")
	}
}

func TestInvalidEnvIntIgnored(t *testing.T) {
	t.Setenv("OSHL_AC_MEMORY_MB", "not-a-number")
	cfg, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MemoryMB != 2048 {
		t.Errorf("memory = %d, want default 2048", cfg.MemoryMB)
	}
}
