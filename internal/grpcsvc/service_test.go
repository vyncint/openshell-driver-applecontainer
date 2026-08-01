package grpcsvc

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/vyncint/openshell-driver-applecontainer/internal/config"
	computev1 "github.com/vyncint/openshell-driver-applecontainer/internal/gen/computev1"
	"github.com/vyncint/openshell-driver-applecontainer/internal/state"
)

func testConfig() config.Config {
	return config.Config{
		Socket:       "/tmp/oshl-ac-test/driver.sock",
		Network:      "oshl",
		DefaultImage: "ghcr.io/nvidia/openshell-community/sandboxes/base:latest",
		Namespace:    "default",
		CPUs:         2,
		MemoryMB:     2048,
		LogLevel:     "debug",
	}
}

// dialTestServer serves srv over bufconn and returns a connected client.
func dialTestServer(t *testing.T, srv *Server) computev1.ComputeDriverClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	computev1.RegisterComputeDriverServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	conn, err := grpc.NewClient("passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return computev1.NewComputeDriverClient(conn)
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	store, err := state.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return New(testConfig(), nil, store, slog.Default(), "test")
}

func TestGetCapabilities(t *testing.T) {
	client := dialTestServer(t, newTestServer(t))
	resp, err := client.GetCapabilities(context.Background(), &computev1.GetCapabilitiesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetDriverName() != "applecontainer" {
		t.Errorf("driver name = %q", resp.GetDriverName())
	}
	if resp.GetDriverVersion() != "test" {
		t.Errorf("driver version = %q", resp.GetDriverVersion())
	}
	if resp.GetDefaultImage() != testConfig().DefaultImage {
		t.Errorf("default image = %q", resp.GetDefaultImage())
	}
}

func TestValidateSandboxCreate(t *testing.T) {
	client := dialTestServer(t, newTestServer(t))
	ctx := context.Background()

	valid := &computev1.DriverSandbox{Id: "0195c1a2-0000-0000-0000-000000000001", Name: "sb"}
	if _, err := client.ValidateSandboxCreate(ctx, &computev1.ValidateSandboxCreateRequest{Sandbox: valid}); err != nil {
		t.Errorf("valid sandbox rejected: %v", err)
	}

	cases := []struct {
		name string
		sb   *computev1.DriverSandbox
	}{
		{"nil sandbox", nil},
		{"invalid id", &computev1.DriverSandbox{Id: "../escape"}},
		{"empty id", &computev1.DriverSandbox{}},
		{"gpu requested", &computev1.DriverSandbox{
			Id: "0195c1a2-0000-0000-0000-000000000002",
			Spec: &computev1.DriverSandboxSpec{
				ResourceRequirements: &computev1.ResourceRequirements{
					Gpu: &computev1.GpuResourceRequirements{},
				},
			},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.ValidateSandboxCreate(ctx, &computev1.ValidateSandboxCreateRequest{Sandbox: tc.sb})
			if status.Code(err) != codes.InvalidArgument {
				t.Errorf("want InvalidArgument, got %v", err)
			}
		})
	}
}

func TestCreateSandboxRequiresEndpoint(t *testing.T) {
	srv := newTestServer(t) // testConfig has no GRPCEndpoint by default
	client := dialTestServer(t, srv)
	_, err := client.CreateSandbox(context.Background(), &computev1.CreateSandboxRequest{
		Sandbox: &computev1.DriverSandbox{Id: "0195c1a2-0000-0000-0000-000000000003", Name: "sb"},
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("want FailedPrecondition without --grpc-endpoint, got %v", err)
	}
}

func TestGetSandboxNotFound(t *testing.T) {
	client := dialTestServer(t, newTestServer(t))
	_, err := client.GetSandbox(context.Background(), &computev1.GetSandboxRequest{SandboxId: "missing"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("want NotFound, got %v", err)
	}
}

func TestListSandboxesEmpty(t *testing.T) {
	client := dialTestServer(t, newTestServer(t))
	resp, err := client.ListSandboxes(context.Background(), &computev1.ListSandboxesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetSandboxes()) != 0 {
		t.Errorf("want empty list, got %d", len(resp.GetSandboxes()))
	}
}

func TestWatchStreamsPublishedEvents(t *testing.T) {
	srv := newTestServer(t)
	client := dialTestServer(t, srv)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := client.WatchSandboxes(ctx, &computev1.WatchSandboxesRequest{})
	if err != nil {
		t.Fatal(err)
	}

	// Give the stream a moment to register its subscription before publishing.
	deadline := time.Now().Add(2 * time.Second)
	for {
		srv.hub.mu.Lock()
		n := len(srv.hub.subs)
		srv.hub.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("watch stream never subscribed")
		}
		time.Sleep(5 * time.Millisecond)
	}

	e := &entry{rec: state.Record{ID: "sb-1", Name: "one", ContainerName: "oshl-sb-1"}, cond: startingCondition()}
	srv.mu.Lock()
	srv.sandboxes["sb-1"] = e
	srv.mu.Unlock()
	srv.publishSandbox(e)

	ev, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	sb := ev.GetSandbox().GetSandbox()
	if sb.GetId() != "sb-1" {
		t.Errorf("event sandbox id = %q", sb.GetId())
	}
	ready := sb.GetStatus().GetConditions()[0]
	if ready.GetType() != "Ready" || ready.GetStatus() != "False" || ready.GetReason() != "Starting" {
		t.Errorf("condition = %+v", ready)
	}

	srv.publishDeleted("sb-1")
	ev, err = stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if ev.GetDeleted().GetSandboxId() != "sb-1" {
		t.Errorf("deleted event = %+v", ev)
	}

	cancel()
	if _, err := stream.Recv(); err == nil {
		t.Error("stream should end after cancel")
	} else if s, ok := status.FromError(err); ok && s.Code() != codes.Canceled && !errors.Is(err, context.Canceled) {
		t.Logf("stream ended with %v (acceptable)", err)
	}
}

func TestWatchReplaysSnapshotOnSubscribe(t *testing.T) {
	srv := newTestServer(t)
	e := &entry{rec: state.Record{ID: "sb-2", Name: "two", ContainerName: "oshl-sb-2"}, cond: readyTrueCondition()}
	srv.mu.Lock()
	srv.sandboxes["sb-2"] = e
	srv.mu.Unlock()

	client := dialTestServer(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := client.WatchSandboxes(ctx, &computev1.WatchSandboxesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	ev, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	sb := ev.GetSandbox().GetSandbox()
	if sb.GetId() != "sb-2" || sb.GetStatus().GetConditions()[0].GetStatus() != "True" {
		t.Errorf("replayed snapshot = %+v", sb)
	}
}
