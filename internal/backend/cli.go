package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CommandRunner executes one external command and returns its output.
type CommandRunner interface {
	Run(ctx context.Context, name string, args []string) (stdout, stderr []byte, err error)
}

// ExecRunner runs commands with os/exec and logs each invocation with its
// duration at debug level.
type ExecRunner struct {
	Log *slog.Logger
}

func (r ExecRunner) Run(ctx context.Context, name string, args []string) ([]byte, []byte, error) {
	start := time.Now()
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if r.Log != nil {
		r.Log.Debug("exec",
			"cmd", name+" "+strings.Join(args, " "),
			"duration", time.Since(start).Round(time.Millisecond).String(),
			"err", err,
		)
	}
	return stdout.Bytes(), stderr.Bytes(), err
}

// CLI is the Runtime implementation backed by the apple/container CLI.
type CLI struct {
	Bin    string // container binary, default "container"
	Runner CommandRunner
}

// NewCLI returns a Runtime that shells out to the `container` binary.
func NewCLI(log *slog.Logger) *CLI {
	return &CLI{Bin: "container", Runner: ExecRunner{Log: log}}
}

func (c *CLI) run(ctx context.Context, args ...string) ([]byte, error) {
	stdout, stderr, err := c.Runner.Run(ctx, c.Bin, args)
	if err != nil {
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = strings.TrimSpace(string(stdout))
		}
		if isNotFound(msg) {
			return stdout, fmt.Errorf("%w: %s %s: %s", ErrNotFound, c.Bin, strings.Join(args, " "), msg)
		}
		return stdout, fmt.Errorf("%s %s: %w: %s", c.Bin, strings.Join(args, " "), err, msg)
	}
	return stdout, nil
}

// isNotFound sniffs the CLI's not-found phrasing so callers can distinguish
// missing resources from real failures.
func isNotFound(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "not found") || strings.Contains(s, "does not exist") || strings.Contains(s, "no such")
}

// RunArgs builds the argv for `container run` from a RunSpec. Exposed for
// golden tests; map-derived flags are emitted in sorted key order so the
// vector is deterministic.
func RunArgs(spec RunSpec) []string {
	args := []string{"run", "--detach", "--name", spec.Name}
	if spec.Network != "" {
		args = append(args, "--network", spec.Network)
	}
	for _, v := range spec.Volumes {
		val := v.HostPath + ":" + v.GuestPath
		if v.ReadOnly {
			val += ":ro"
		}
		args = append(args, "--volume", val)
	}
	for _, t := range spec.Tmpfs {
		args = append(args, "--tmpfs", t)
	}
	for _, k := range sortedKeys(spec.Env) {
		args = append(args, "--env", k+"="+spec.Env[k])
	}
	for _, k := range sortedKeys(spec.Labels) {
		args = append(args, "--label", k+"="+spec.Labels[k])
	}
	if spec.CPUs > 0 {
		args = append(args, "--cpus", strconv.FormatInt(spec.CPUs, 10))
	}
	if spec.MemoryMB > 0 {
		args = append(args, "--memory", strconv.FormatInt(spec.MemoryMB, 10)+"M")
	}
	if spec.UID != nil {
		args = append(args, "--uid", strconv.FormatInt(*spec.UID, 10))
	}
	if spec.GID != nil {
		args = append(args, "--gid", strconv.FormatInt(*spec.GID, 10))
	}
	for _, c := range spec.CapAdd {
		args = append(args, "--cap-add", c)
	}
	if spec.Kernel != "" {
		args = append(args, "--kernel", spec.Kernel)
	}
	if spec.Entrypoint != "" {
		args = append(args, "--entrypoint", spec.Entrypoint)
	}
	args = append(args, spec.Image)
	args = append(args, spec.Args...)
	return args
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (c *CLI) Run(ctx context.Context, spec RunSpec) (string, error) {
	out, err := c.run(ctx, RunArgs(spec)...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (c *CLI) Delete(ctx context.Context, name string) error {
	_, err := c.run(ctx, "delete", "--force", name)
	return err
}

func (c *CLI) Stop(ctx context.Context, name string) error {
	_, err := c.run(ctx, "stop", name)
	return err
}

// lsEntry mirrors the subset of `container ls --format json` the driver
// consumes.
type lsEntry struct {
	ID     string `json:"id"`
	Status struct {
		State    string `json:"state"`
		Started  string `json:"startedDate"`
		Networks []struct {
			Network     string `json:"network"`
			Hostname    string `json:"hostname"`
			IPv4Address string `json:"ipv4Address"`
			IPv4Gateway string `json:"ipv4Gateway"`
		} `json:"networks"`
	} `json:"status"`
	Configuration struct {
		CreationDate string `json:"creationDate"`
		Image        struct {
			Reference  string `json:"reference"`
			Descriptor struct {
				Digest string `json:"digest"`
			} `json:"descriptor"`
		} `json:"image"`
		Labels    map[string]string `json:"labels"`
		Resources struct {
			CPUs          int64 `json:"cpus"`
			MemoryInBytes int64 `json:"memoryInBytes"`
		} `json:"resources"`
	} `json:"configuration"`
}

func (e lsEntry) toContainer() Container {
	ctr := Container{
		ID:          e.ID,
		State:       e.Status.State,
		ImageRef:    e.Configuration.Image.Reference,
		ImageDigest: e.Configuration.Image.Descriptor.Digest,
		Labels:      e.Configuration.Labels,
		CPUs:        e.Configuration.Resources.CPUs,
		MemoryBytes: e.Configuration.Resources.MemoryInBytes,
		CreatedAt:   e.Configuration.CreationDate,
		StartedAt:   e.Status.Started,
	}
	for _, n := range e.Status.Networks {
		ctr.Networks = append(ctr.Networks, NetworkAttachment{
			Network:     n.Network,
			Hostname:    n.Hostname,
			IPv4Address: n.IPv4Address,
			IPv4Gateway: n.IPv4Gateway,
		})
	}
	return ctr
}

// ParseContainerList decodes `container ls --format json` output. Exposed
// for tests against recorded fixtures.
func ParseContainerList(data []byte) ([]Container, error) {
	var entries []lsEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse container list: %w", err)
	}
	out := make([]Container, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.toContainer())
	}
	return out, nil
}

func (c *CLI) List(ctx context.Context, all bool) ([]Container, error) {
	args := []string{"ls", "--format", "json"}
	if all {
		args = []string{"ls", "--all", "--format", "json"}
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	return ParseContainerList(out)
}

func (c *CLI) Get(ctx context.Context, name string) (Container, error) {
	list, err := c.List(ctx, true)
	if err != nil {
		return Container{}, err
	}
	for _, ctr := range list {
		if ctr.ID == name {
			return ctr, nil
		}
	}
	return Container{}, fmt.Errorf("%w: container %q", ErrNotFound, name)
}

// imageEntry mirrors the subset of `container image ls --format json` used:
// the reference lives at configuration.name, and the OCI config (USER) in
// per-platform variants.
type imageEntry struct {
	Configuration struct {
		Name       string `json:"name"`
		Descriptor struct {
			Digest string `json:"digest"`
		} `json:"descriptor"`
	} `json:"configuration"`
	Variants []struct {
		Config struct {
			Architecture string `json:"architecture"`
			Config       struct {
				User string `json:"User"`
			} `json:"config"`
		} `json:"config"`
	} `json:"variants"`
}

func (e imageEntry) user() string {
	fallback := ""
	for i, v := range e.Variants {
		if v.Config.Architecture == "arm64" {
			return v.Config.Config.User
		}
		if i == 0 {
			fallback = v.Config.Config.User
		}
	}
	return fallback
}

// ParseImageList decodes `container image ls --format json` output.
func ParseImageList(data []byte) ([]Image, error) {
	var entries []imageEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse image list: %w", err)
	}
	out := make([]Image, 0, len(entries))
	for _, e := range entries {
		out = append(out, Image{
			Reference: e.Configuration.Name,
			Digest:    e.Configuration.Descriptor.Digest,
			User:      e.user(),
		})
	}
	return out, nil
}

func (c *CLI) ImageList(ctx context.Context) ([]Image, error) {
	out, err := c.run(ctx, "image", "ls", "--format", "json")
	if err != nil {
		return nil, err
	}
	return ParseImageList(out)
}

func (c *CLI) ImagePull(ctx context.Context, ref, platform string) error {
	args := []string{"image", "pull", ref}
	if platform != "" {
		args = append(args, "--platform", platform)
	}
	_, err := c.run(ctx, args...)
	return err
}

func (c *CLI) Create(ctx context.Context, name, image string) (string, error) {
	out, err := c.run(ctx, "create", "--name", name, image)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (c *CLI) CopyFrom(ctx context.Context, name, guestPath, hostPath string) error {
	_, err := c.run(ctx, "cp", name+":"+guestPath, hostPath)
	return err
}

// ParseNetworkList decodes `container network ls --format json` output.
func ParseNetworkList(data []byte) ([]Network, error) {
	var entries []struct {
		ID     string `json:"id"`
		Status struct {
			IPv4Gateway string `json:"ipv4Gateway"`
			IPv4Subnet  string `json:"ipv4Subnet"`
		} `json:"status"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse network list: %w", err)
	}
	out := make([]Network, 0, len(entries))
	for _, e := range entries {
		out = append(out, Network{
			Name:        e.ID,
			IPv4Gateway: e.Status.IPv4Gateway,
			IPv4Subnet:  e.Status.IPv4Subnet,
		})
	}
	return out, nil
}

func (c *CLI) Networks(ctx context.Context) ([]Network, error) {
	out, err := c.run(ctx, "network", "ls", "--format", "json")
	if err != nil {
		return nil, err
	}
	return ParseNetworkList(out)
}

func (c *CLI) NetworkCreate(ctx context.Context, name string) error {
	_, err := c.run(ctx, "network", "create", name)
	return err
}

func (c *CLI) SystemStart(ctx context.Context) error {
	_, err := c.run(ctx, "system", "start")
	return err
}

func (c *CLI) Logs(ctx context.Context, name string, tail int) (string, error) {
	args := []string{"logs"}
	if tail > 0 {
		args = append(args, "-n", strconv.Itoa(tail))
	}
	args = append(args, name)
	out, err := c.run(ctx, args...)
	if err != nil {
		return "", err
	}
	return string(out), nil
}
