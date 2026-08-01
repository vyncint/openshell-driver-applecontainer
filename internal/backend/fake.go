package backend

import (
	"context"
	"fmt"
	"os"
	"sync"
)

// Fake is an in-memory Runtime for unit tests. Zero value is usable.
type Fake struct {
	mu         sync.Mutex
	containers map[string]*fakeContainer
	images     []Image
	networks   []Network
	runCalls   []RunSpec
	pulls      []string

	// RunError, when set, fails the next Run call.
	RunError error
	// RunBlock, when non-nil, is received from inside Run before the
	// container is registered — lets tests hold a create in flight.
	RunBlock chan struct{}
	// CopySrc, when set, is copied to the destination by CopyFrom;
	// otherwise a small placeholder ELF header is written.
	CopySrc []byte
	// NetworkListError, when set, fails NetworkList calls (simulates an
	// unreachable container runtime).
	NetworkListError error
}

type fakeContainer struct {
	ctr  Container
	spec RunSpec
	logs string
}

func (f *Fake) init() {
	if f.containers == nil {
		f.containers = make(map[string]*fakeContainer)
	}
}

// AddImage registers a locally available image.
func (f *Fake) AddImage(ref, digest string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.images = append(f.images, Image{Reference: ref, Digest: digest})
}

// AddImageWithUser registers a locally available image with an OCI USER.
func (f *Fake) AddImageWithUser(ref, digest, user string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.images = append(f.images, Image{Reference: ref, Digest: digest, User: user})
}

// RunCalls returns a copy of every RunSpec passed to Run.
func (f *Fake) RunCalls() []RunSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]RunSpec, len(f.runCalls))
	copy(out, f.runCalls)
	return out
}

// Pulls returns the refs passed to ImagePull.
func (f *Fake) Pulls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.pulls))
	copy(out, f.pulls)
	return out
}

// SetState overrides a container's state (e.g. "stopped").
func (f *Fake) SetState(name, s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.containers[name]; ok {
		c.ctr.State = s
	}
}

// SetLogs sets the console output returned by Logs for a container.
func (f *Fake) SetLogs(name, logs string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.containers[name]; ok {
		c.logs = logs
	}
}

func (f *Fake) Run(ctx context.Context, spec RunSpec) (string, error) {
	if f.RunBlock != nil {
		select {
		case <-f.RunBlock:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	f.runCalls = append(f.runCalls, spec)
	if f.RunError != nil {
		err := f.RunError
		f.RunError = nil
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f.containers[spec.Name] = &fakeContainer{
		spec: spec,
		ctr: Container{
			ID:       spec.Name,
			State:    "running",
			ImageRef: spec.Image,
			Labels:   spec.Labels,
			Networks: []NetworkAttachment{{
				Network:     spec.Network,
				Hostname:    spec.Name,
				IPv4Address: "192.168.65.9/24",
				IPv4Gateway: "192.168.65.1",
			}},
			CPUs:        spec.CPUs,
			MemoryBytes: spec.MemoryMB << 20,
		},
	}
	return spec.Name, nil
}

func (f *Fake) Delete(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	if _, ok := f.containers[name]; !ok {
		return fmt.Errorf("%w: container %q", ErrNotFound, name)
	}
	delete(f.containers, name)
	return nil
}

func (f *Fake) Stop(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	c, ok := f.containers[name]
	if !ok {
		return fmt.Errorf("%w: container %q", ErrNotFound, name)
	}
	c.ctr.State = "stopped"
	return nil
}

func (f *Fake) List(_ context.Context, all bool) ([]Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Container
	for _, c := range f.containers {
		if !all && c.ctr.State != "running" {
			continue
		}
		out = append(out, c.ctr)
	}
	return out, nil
}

func (f *Fake) Get(_ context.Context, name string) (Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[name]
	if !ok {
		return Container{}, fmt.Errorf("%w: container %q", ErrNotFound, name)
	}
	return c.ctr, nil
}

func (f *Fake) ImageList(_ context.Context) ([]Image, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Image, len(f.images))
	copy(out, f.images)
	return out, nil
}

func (f *Fake) ImagePull(_ context.Context, ref, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulls = append(f.pulls, ref)
	f.images = append(f.images, Image{Reference: ref, Digest: "sha256:pulled" + fmt.Sprintf("%04d", len(f.pulls))})
	return nil
}

func (f *Fake) Create(_ context.Context, name, image string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	f.containers[name] = &fakeContainer{ctr: Container{ID: name, State: "created", ImageRef: image}}
	return name, nil
}

func (f *Fake) CopyFrom(_ context.Context, name, _, hostPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.containers[name]; !ok {
		return fmt.Errorf("%w: container %q", ErrNotFound, name)
	}
	data := f.CopySrc
	if data == nil {
		data = []byte{0x7f, 'E', 'L', 'F', 0, 0, 0, 0}
	}
	return os.WriteFile(hostPath, data, 0o644)
}

func (f *Fake) Networks(_ context.Context) ([]Network, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.NetworkListError != nil {
		return nil, f.NetworkListError
	}
	out := make([]Network, len(f.networks))
	copy(out, f.networks)
	return out, nil
}

// NetworkCreate registers the network with the address layout vmnet uses
// on a real host (host-side gateway at .1).
func (f *Fake) NetworkCreate(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.networks = append(f.networks, Network{
		Name:        name,
		IPv4Gateway: "192.168.65.1",
		IPv4Subnet:  "192.168.65.0/24",
	})
	return nil
}

// AddNetwork registers a pre-existing network.
func (f *Fake) AddNetwork(n Network) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.networks = append(f.networks, n)
}

func (f *Fake) SystemStart(_ context.Context) error {
	return nil
}

func (f *Fake) Logs(_ context.Context, name string, _ int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[name]
	if !ok {
		return "", fmt.Errorf("%w: container %q", ErrNotFound, name)
	}
	return c.logs, nil
}

var _ Runtime = (*Fake)(nil)
