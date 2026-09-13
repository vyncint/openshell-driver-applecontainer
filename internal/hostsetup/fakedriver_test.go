package hostsetup

import (
	"context"
	"net"
	"os"
	"testing"

	"google.golang.org/grpc"

	computev1 "github.com/vyncint/openshell-driver-applecontainer/internal/gen/computev1"
)

// shortTempDir returns a temp dir under /tmp: t.TempDir() paths on macOS
// exceed the sun_path limit for unix sockets.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "oshl-st-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

type fakeDriver struct {
	computev1.UnimplementedComputeDriverServer
	version   string
	sandboxes []*computev1.DriverSandbox
}

func (f *fakeDriver) GetCapabilities(context.Context, *computev1.GetCapabilitiesRequest) (*computev1.GetCapabilitiesResponse, error) {
	return &computev1.GetCapabilitiesResponse{DriverName: "applecontainer", DriverVersion: f.version}, nil
}

func (f *fakeDriver) ListSandboxes(context.Context, *computev1.ListSandboxesRequest) (*computev1.ListSandboxesResponse, error) {
	return &computev1.ListSandboxesResponse{Sandboxes: f.sandboxes}, nil
}

// serveFakeDriver answers the two RPCs status uses on a unix socket.
func serveFakeDriver(t *testing.T, socket, version string, sandboxes []*computev1.DriverSandbox) func() {
	t.Helper()
	lis, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	gs := grpc.NewServer()
	computev1.RegisterComputeDriverServer(gs, &fakeDriver{version: version, sandboxes: sandboxes})
	go func() { _ = gs.Serve(lis) }()
	return gs.Stop
}
