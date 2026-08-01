package grpcsvc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/vyncint/openshell-driver-applecontainer/internal/seed"
)

// reservedMountTargets are container paths user mounts may not shadow —
// the upstream list plus this driver's seed mount. A target at or under
// any of these is rejected.
var reservedMountTargets = []string{
	"/opt/openshell",
	"/etc/openshell",
	"/etc/openshell-tls",
	"/run/netns",
	"/sandbox",
	seed.GuestSeedDir,
}

// driverConfig is the schema of the per-sandbox
// --driver-config-json '{"applecontainer": {...}}' block. Unknown keys are
// rejected, matching upstream drivers' deny_unknown_fields.
type driverConfig struct {
	// Mounts adds volume (host directory) or tmpfs mounts.
	Mounts []mountConfig `json:"mounts"`
	// Network overrides the driver's vmnet network for this sandbox.
	Network string `json:"network"`
	// Kernel is a host path passed through to `container run --kernel` —
	// e.g. a build with Landlock enabled.
	Kernel string `json:"kernel"`
}

type mountConfig struct {
	// Type is "volume" (host directory bind; apple/container has no named
	// volumes, so source is a host path) or "tmpfs".
	Type string `json:"type"`
	// Source is the host directory for volume mounts.
	Source string `json:"source,omitempty"`
	// Target is the absolute container path.
	Target string `json:"target"`
	// ReadOnly defaults to true for volume mounts.
	ReadOnly *bool `json:"read_only,omitempty"`
}

func (m mountConfig) readOnly() bool {
	if m.ReadOnly == nil {
		return true
	}
	return *m.ReadOnly
}

// parseDriverConfig decodes and validates the driver_config Struct. A nil
// or empty struct yields the zero config.
func parseDriverConfig(st *structpb.Struct) (driverConfig, error) {
	var cfg driverConfig
	if st == nil || len(st.GetFields()) == 0 {
		return cfg, nil
	}
	blob, err := protojson.Marshal(st)
	if err != nil {
		return cfg, fmt.Errorf("driver config: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(blob))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("driver config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c driverConfig) validate() error {
	seen := make(map[string]bool, len(c.Mounts))
	for i, m := range c.Mounts {
		if err := validateMountTarget(m.Target); err != nil {
			return fmt.Errorf("driver config: mounts[%d]: %w", i, err)
		}
		if seen[m.Target] {
			return fmt.Errorf("driver config: mounts[%d]: duplicate target %q", i, m.Target)
		}
		seen[m.Target] = true
		switch m.Type {
		case "volume":
			if !strings.HasPrefix(m.Source, "/") {
				return fmt.Errorf("driver config: mounts[%d]: volume source must be an absolute host path", i)
			}
		case "tmpfs":
			if m.Source != "" {
				return fmt.Errorf("driver config: mounts[%d]: tmpfs takes no source", i)
			}
		default:
			return fmt.Errorf("driver config: mounts[%d]: unsupported type %q (volume or tmpfs)", i, m.Type)
		}
	}
	if strings.ContainsAny(c.Network, " \t\n") {
		return fmt.Errorf("driver config: invalid network name %q", c.Network)
	}
	if c.Kernel != "" && !strings.HasPrefix(c.Kernel, "/") {
		return fmt.Errorf("driver config: kernel must be an absolute host path")
	}
	return nil
}

// validateMountTarget enforces the reserved-target policy on one container
// path.
func validateMountTarget(target string) error {
	if target == "" {
		return fmt.Errorf("mount target is required")
	}
	if !strings.HasPrefix(target, "/") {
		return fmt.Errorf("mount target %q must be absolute", target)
	}
	if path.Clean(target) != target {
		return fmt.Errorf("mount target %q must be a normalized path", target)
	}
	if target == "/" {
		return fmt.Errorf("mount target may not be the container root")
	}
	for _, r := range reservedMountTargets {
		if target == r || strings.HasPrefix(target, r+"/") {
			return fmt.Errorf("mount target %q shadows reserved path %s", target, r)
		}
	}
	return nil
}
