package backend

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func ptrInt64(v int64) *int64 { return &v }

func TestRunArgs(t *testing.T) {
	tests := []struct {
		name string
		spec RunSpec
		want []string
	}{
		{
			name: "minimal",
			spec: RunSpec{Name: "oshl-a", Image: "alpine:latest"},
			want: []string{"run", "--detach", "--name", "oshl-a", "alpine:latest"},
		},
		{
			name: "full sandbox boot",
			spec: RunSpec{
				Name:    "oshl-abc123",
				Image:   "ghcr.io/example/sandbox@sha256:deadbeef",
				Network: "oshl",
				Volumes: []VolumeMount{
					{HostPath: "/tmp/oshl-ac/seed/abc123", GuestPath: "/openshell-seed", ReadOnly: true},
				},
				Tmpfs: []string{"/run/netns"},
				Env: map[string]string{
					"OPENSHELL_SANDBOX_ID": "abc123",
					"OPENSHELL_ENDPOINT":   "https://192.168.65.1:17670",
				},
				Labels: map[string]string{
					"openshell.ai/sandbox-id": "abc123",
				},
				CPUs:       2,
				MemoryMB:   2048,
				Entrypoint: "/openshell-seed/boot.sh",
			},
			want: []string{
				"run", "--detach", "--name", "oshl-abc123",
				"--network", "oshl",
				"--volume", "/tmp/oshl-ac/seed/abc123:/openshell-seed:ro",
				"--tmpfs", "/run/netns",
				"--env", "OPENSHELL_ENDPOINT=https://192.168.65.1:17670",
				"--env", "OPENSHELL_SANDBOX_ID=abc123",
				"--label", "openshell.ai/sandbox-id=abc123",
				"--cpus", "2",
				"--memory", "2048M",
				"--entrypoint", "/openshell-seed/boot.sh",
				"ghcr.io/example/sandbox@sha256:deadbeef",
			},
		},
		{
			name: "custom kernel and args",
			spec: RunSpec{
				Name:   "oshl-k",
				Image:  "img:1",
				Kernel: "/opt/kernels/landlock-6.18",
				Args:   []string{"--flag", "v"},
			},
			want: []string{
				"run", "--detach", "--name", "oshl-k",
				"--kernel", "/opt/kernels/landlock-6.18",
				"img:1", "--flag", "v",
			},
		},
		{
			name: "root uid gid override",
			spec: RunSpec{
				Name:  "oshl-r",
				Image: "img:1",
				UID:   ptrInt64(0),
				GID:   ptrInt64(0),
			},
			want: []string{
				"run", "--detach", "--name", "oshl-r",
				"--uid", "0", "--gid", "0",
				"img:1",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RunArgs(tt.spec)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("RunArgs mismatch\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

// containerLsFixture is a trimmed real capture of `container ls --format json`
// from apple/container 1.2.0.
const containerLsFixture = `[
  {
    "configuration": {
      "creationDate": "2026-08-01T16:01:27Z",
      "id": "oshl-g05",
      "image": {
        "descriptor": {
          "digest": "sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b",
          "mediaType": "application/vnd.oci.image.index.v1+json",
          "size": 9218
        },
        "reference": "docker.io/library/alpine:latest"
      },
      "labels": {"openshell.ai/sandbox-id": "g05"},
      "networks": [{"network": "oshl", "options": {"hostname": "oshl-g05", "mtu": 1280}}],
      "resources": {"cpuOverhead": 1, "cpus": 4, "memoryInBytes": 1073741824}
    },
    "id": "oshl-g05",
    "status": {
      "networks": [
        {
          "hostname": "oshl-g05",
          "ipv4Address": "192.168.65.2/24",
          "ipv4Gateway": "192.168.65.1",
          "macAddress": "f6:19:42:1e:d4:48",
          "mtu": 1280,
          "network": "oshl"
        }
      ],
      "startedDate": "2026-08-01T16:01:27Z",
      "state": "running"
    }
  }
]`

func TestParseContainerList(t *testing.T) {
	got, err := ParseContainerList([]byte(containerLsFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 container, got %d", len(got))
	}
	c := got[0]
	if c.ID != "oshl-g05" || c.State != "running" {
		t.Errorf("id/state = %q/%q", c.ID, c.State)
	}
	if c.ImageRef != "docker.io/library/alpine:latest" {
		t.Errorf("image ref = %q", c.ImageRef)
	}
	if !strings.HasPrefix(c.ImageDigest, "sha256:28bd5fe8") {
		t.Errorf("digest = %q", c.ImageDigest)
	}
	if c.Labels["openshell.ai/sandbox-id"] != "g05" {
		t.Errorf("labels = %v", c.Labels)
	}
	if c.IPv4() != "192.168.65.2" {
		t.Errorf("IPv4() = %q", c.IPv4())
	}
	if len(c.Networks) != 1 || c.Networks[0].IPv4Gateway != "192.168.65.1" {
		t.Errorf("networks = %+v", c.Networks)
	}
	if c.CPUs != 4 || c.MemoryBytes != 1073741824 {
		t.Errorf("resources = %d cpu / %d bytes", c.CPUs, c.MemoryBytes)
	}
}

func TestParseContainerListEmpty(t *testing.T) {
	got, err := ParseContainerList([]byte(`[]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("want empty, got %v", got)
	}
}

// recordingRunner captures invocations and replays scripted results.
type recordingRunner struct {
	calls   [][]string
	stdout  []byte
	stderr  []byte
	err     error
	perCall map[string]result // keyed by first two args joined by space
}

type result struct {
	stdout []byte
	stderr []byte
	err    error
}

func (r *recordingRunner) Run(_ context.Context, name string, args []string) ([]byte, []byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	if r.perCall != nil {
		key := strings.Join(args[:min(2, len(args))], " ")
		if res, ok := r.perCall[key]; ok {
			return res.stdout, res.stderr, res.err
		}
	}
	return r.stdout, r.stderr, r.err
}

func TestCLIDeleteNotFound(t *testing.T) {
	rr := &recordingRunner{stderr: []byte(`Error: container "nope" not found`), err: errors.New("exit status 1")}
	cli := &CLI{Bin: "container", Runner: rr}
	err := cli.Delete(context.Background(), "nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	want := []string{"container", "delete", "--force", "nope"}
	if !reflect.DeepEqual(rr.calls[0], want) {
		t.Errorf("argv = %q, want %q", rr.calls[0], want)
	}
}

func TestCLIGet(t *testing.T) {
	rr := &recordingRunner{stdout: []byte(containerLsFixture)}
	cli := &CLI{Bin: "container", Runner: rr}
	c, err := cli.Get(context.Background(), "oshl-g05")
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != "oshl-g05" {
		t.Errorf("id = %q", c.ID)
	}
	if _, err := cli.Get(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound for missing container, got %v", err)
	}
	want := []string{"container", "ls", "--all", "--format", "json"}
	if !reflect.DeepEqual(rr.calls[0], want) {
		t.Errorf("argv = %q, want %q", rr.calls[0], want)
	}
}

// imageLsFixture is a trimmed real capture of `container image ls --format
// json` from apple/container 1.2.0 — the reference lives at
// configuration.name, not at a top-level reference field.
const imageLsFixture = `[
  {
    "configuration": {
      "creationDate": "2026-07-31T15:10:14Z",
      "descriptor": {
        "digest": "sha256:eca343a8a4ffb874ba6256ebd3a12f7e1f9f186e1b3518bfafd6fd2b68670a62",
        "mediaType": "application/vnd.oci.image.index.v1+json",
        "size": 645
      },
      "name": "ghcr.io/nvidia/openshell/supervisor:0.0.96"
    },
    "id": "eca343a8a4ffb874ba6256ebd3a12f7e1f9f186e1b3518bfafd6fd2b68670a62"
  }
]`

func TestParseImageList(t *testing.T) {
	got, err := ParseImageList([]byte(imageLsFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 image, got %d", len(got))
	}
	if got[0].Reference != "ghcr.io/nvidia/openshell/supervisor:0.0.96" {
		t.Errorf("reference = %q", got[0].Reference)
	}
	if !strings.HasPrefix(got[0].Digest, "sha256:eca343a8") {
		t.Errorf("digest = %q", got[0].Digest)
	}
}

func TestCLIRunReturnsID(t *testing.T) {
	rr := &recordingRunner{stdout: []byte("oshl-x\n")}
	cli := &CLI{Bin: "container", Runner: rr}
	id, err := cli.Run(context.Background(), RunSpec{Name: "oshl-x", Image: "img"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "oshl-x" {
		t.Errorf("id = %q", id)
	}
}
