// Package backend abstracts the apple/container CLI behind a Runtime
// interface so the rest of the driver can be unit-tested against a fake.
package backend

import (
	"context"
	"errors"
)

// ErrNotFound is returned when the runtime does not know the requested
// container or image.
var ErrNotFound = errors.New("backend: not found")

// VolumeMount is a host directory bind-mounted into the guest.
type VolumeMount struct {
	HostPath  string
	GuestPath string
	ReadOnly  bool
}

// RunSpec describes one `container run` invocation for a sandbox VM.
type RunSpec struct {
	Name       string
	Image      string
	Network    string
	Volumes    []VolumeMount
	Tmpfs      []string
	Env        map[string]string
	Labels     map[string]string
	CPUs       int64  // vCPU count; 0 means runtime default
	MemoryMB   int64  // mebibytes; 0 means runtime default
	Entrypoint string // optional entrypoint override
	Args       []string
	Kernel     string // optional custom kernel path (container run -k)
	// UID/GID override the image's USER for the init process. The
	// supervisor must run as guest root regardless of the image user.
	UID *int64
	GID *int64
	// CapAdd extends the default OCI capability set. Even as uid 0 the
	// guest init applies default caps, which exclude SYS_ADMIN/NET_ADMIN.
	CapAdd []string
}

// NetworkAttachment is one network interface of a running container.
type NetworkAttachment struct {
	Network     string
	Hostname    string
	IPv4Address string // CIDR form, e.g. "192.168.65.2/24"
	IPv4Gateway string
}

// Container is the runtime-observed state of one container VM.
type Container struct {
	ID          string
	State       string // e.g. "running", "stopped"
	ImageRef    string
	ImageDigest string
	Labels      map[string]string
	Networks    []NetworkAttachment
	CPUs        int64
	MemoryBytes int64
	CreatedAt   string
	StartedAt   string
}

// IPv4 returns the bare IPv4 address of the first network attachment,
// without the CIDR suffix, or "" when the container has no address.
func (c Container) IPv4() string {
	for _, n := range c.Networks {
		addr := n.IPv4Address
		for i := 0; i < len(addr); i++ {
			if addr[i] == '/' {
				return addr[:i]
			}
		}
		if addr != "" {
			return addr
		}
	}
	return ""
}

// Image is one locally available image.
type Image struct {
	Reference string
	Digest    string
	// User is the OCI config USER of the platform variant ("" when unset).
	User string
}

// Runtime is the complete surface of the apple/container CLI used by the
// driver. Every `container …` invocation goes through this interface.
type Runtime interface {
	// Run starts a detached container VM and returns its ID.
	Run(ctx context.Context, spec RunSpec) (string, error)
	// Delete force-removes a container (running or not). Deleting an
	// unknown container returns ErrNotFound.
	Delete(ctx context.Context, name string) error
	// Stop stops a running container without deleting it.
	Stop(ctx context.Context, name string) error
	// List returns containers; all=true includes non-running ones.
	List(ctx context.Context, all bool) ([]Container, error)
	// Get returns a single container by name.
	Get(ctx context.Context, name string) (Container, error)

	// ImageList returns locally available images.
	ImageList(ctx context.Context) ([]Image, error)
	// ImagePull pulls an image for the given platform (e.g. "linux/arm64").
	ImagePull(ctx context.Context, ref, platform string) error

	// Create creates (without starting) a container and returns its ID.
	// Used for extracting files from images.
	Create(ctx context.Context, name, image string) (string, error)
	// CopyFrom copies a path out of a (created or running) container to the
	// host. Note: apple/container cp does not preserve the executable bit.
	CopyFrom(ctx context.Context, name, guestPath, hostPath string) error

	// NetworkList returns the names of configured vmnet networks.
	NetworkList(ctx context.Context) ([]string, error)
	// NetworkCreate creates a vmnet network with the given name.
	NetworkCreate(ctx context.Context, name string) error

	// Logs returns the last tail lines of a container's console output
	// (all output when tail <= 0). Works on stopped containers.
	Logs(ctx context.Context, name string, tail int) (string, error)
}
