package config

import (
	"io"
	"log/slog"
	"testing"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// stubGatewayVersion swaps the version probe for the duration of a test.
func stubGatewayVersion(t *testing.T, v string) {
	t.Helper()
	prev := gatewayVersionProbe
	gatewayVersionProbe = func() string { return v }
	t.Cleanup(func() { gatewayVersionProbe = prev })
}

// The whole point of P0: a gateway newer than the pinned tag must pull the
// matching supervisor, not the stale one.
func TestResolveSupervisorImageMatchesGateway(t *testing.T) {
	stubGatewayVersion(t, "0.0.97")
	cfg := Config{SupervisorImage: PinnedSupervisorImage()}

	cfg.ResolveSupervisorImage(quietLog())

	want := SupervisorRepo + ":0.0.97"
	if cfg.SupervisorImage != want {
		t.Errorf("supervisor image = %q, want %q", cfg.SupervisorImage, want)
	}
}

// An operator who pinned the image keeps it, whatever the gateway reports.
func TestResolveSupervisorImageRespectsExplicitPin(t *testing.T) {
	stubGatewayVersion(t, "0.0.97")
	cfg := Config{SupervisorImage: "example.com/mine:custom", SupervisorImageExplicit: true}

	cfg.ResolveSupervisorImage(quietLog())

	if cfg.SupervisorImage != "example.com/mine:custom" {
		t.Errorf("explicit pin overwritten: %q", cfg.SupervisorImage)
	}
}

// No readable gateway (not installed, odd output) must not blank the image.
func TestResolveSupervisorImageFallsBackWhenGatewayUnknown(t *testing.T) {
	stubGatewayVersion(t, "")
	cfg := Config{SupervisorImage: PinnedSupervisorImage()}

	cfg.ResolveSupervisorImage(quietLog())

	if cfg.SupervisorImage != PinnedSupervisorImage() {
		t.Errorf("expected the pinned fallback, got %q", cfg.SupervisorImage)
	}
}

// Parse must record explicitness so the resolver knows to stay out of the way.
func TestParseRecordsSupervisorImageExplicit(t *testing.T) {
	cfg, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SupervisorImageExplicit {
		t.Error("no flag/env set: should not be marked explicit")
	}

	cfg, err = Parse([]string{"--supervisor-image", "example.com/x:1"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.SupervisorImageExplicit {
		t.Error("--supervisor-image set: should be marked explicit")
	}

	t.Setenv("OSHL_AC_SUPERVISOR_IMAGE", "example.com/env:2")
	cfg, err = Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.SupervisorImageExplicit {
		t.Error("env var set: should be marked explicit")
	}
}
