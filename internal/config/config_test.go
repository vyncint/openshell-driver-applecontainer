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
	if cfg.SupervisorImage != "ghcr.io/nvidia/openshell/supervisor:0.0.96" {
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
