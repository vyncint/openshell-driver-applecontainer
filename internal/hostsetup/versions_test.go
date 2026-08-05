package hostsetup

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestProbeVersion(t *testing.T) {
	cases := map[string]struct{ out, want string }{
		"gateway style":      {"openshell-gateway 0.0.97\n", "0.0.97"},
		"container style":    {"container CLI version 1.2.0 (build: release, commit: 6e65319)\n", "1.2.0"},
		"v prefix":           {"driver v0.2.6 (commit abc)\n", "0.2.6"},
		"nothing version-ay": {"weird output\n", ""},
	}
	for name, c := range cases {
		got := probeVersion(func(string, ...string) (string, error) { return c.out, nil }, "x")
		if got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
	// A missing binary must not panic or invent a version.
	if got := probeVersion(func(string, ...string) (string, error) { return "", errors.New("not found") }, "x"); got != "" {
		t.Errorf("missing binary: got %q, want empty", got)
	}
}

// The whole point of P2: a supervisor tag that lags the gateway is called out
// instead of failing silently at first create.
func TestLogVersionsWarnsOnMismatch(t *testing.T) {
	var buf bytes.Buffer
	s := &Setup{
		Log: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Exec: func(name string, _ ...string) (string, error) {
			if name == "openshell-gateway" {
				return "openshell-gateway 0.0.97\n", nil
			}
			return "container CLI version 1.2.0 (build: release)\n", nil
		},
	}

	s.logVersions(Options{DriverVersion: "v0.2.6", SupervisorImage: "ghcr.io/nvidia/openshell/supervisor:0.0.96"})

	out := buf.String()
	if !strings.Contains(out, "does not match") {
		t.Errorf("expected a mismatch warning, got: %s", out)
	}
	for _, want := range []string{"0.0.97", "1.2.0", "v0.2.6"} {
		if !strings.Contains(out, want) {
			t.Errorf("version summary missing %q: %s", want, out)
		}
	}
}

func TestLogVersionsQuietWhenMatched(t *testing.T) {
	var buf bytes.Buffer
	s := &Setup{
		Log: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Exec: func(name string, _ ...string) (string, error) {
			if name == "openshell-gateway" {
				return "openshell-gateway 0.0.97\n", nil
			}
			return "container CLI version 1.2.0\n", nil
		},
	}

	s.logVersions(Options{DriverVersion: "v0.2.6", SupervisorImage: "ghcr.io/nvidia/openshell/supervisor:0.0.97"})

	if strings.Contains(buf.String(), "does not match") {
		t.Errorf("matched versions should not warn: %s", buf.String())
	}
}
