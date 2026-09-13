package grpcsvc

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/vyncint/openshell-driver-applecontainer/internal/backend"
	computev1 "github.com/vyncint/openshell-driver-applecontainer/internal/gen/computev1"
)

// readySandbox creates the test sandbox and waits until its VM is running
// and provisioning has fully finished.
func readySandbox(t *testing.T, fake *backend.Fake) (*Server, computev1.ComputeDriverClient) {
	t.Helper()
	srv := newLiveServer(t, fake)
	client := dialTestServer(t, srv)
	if _, err := client.CreateSandbox(context.Background(), createRequest()); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, srv, reasonBackendReady)
	waitForProvisioning(t, srv)
	return srv, client
}

func TestStopAndStartSandbox(t *testing.T) {
	fake := &backend.Fake{}
	srv, client := readySandbox(t, fake)
	ctx := context.Background()
	name := "oshl-" + testSandboxID

	subID, events := srv.hub.subscribe()
	defer srv.hub.unsubscribe(subID)

	if _, err := client.StopSandbox(ctx, &computev1.StopSandboxRequest{SandboxId: testSandboxID}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if got := fake.Stops(); len(got) != 1 || got[0] != name {
		t.Errorf("stop calls = %v", got)
	}
	c, err := fake.Get(ctx, name)
	if err != nil || c.State != "stopped" {
		t.Errorf("VM after stop = %+v, %v", c, err)
	}
	cond := waitForCondition(t, srv, reasonContainerStopped)
	if cond.Status != "False" {
		t.Errorf("stopped condition = %+v", cond)
	}
	// The intent is persisted, so a restart keeps reporting "stopped".
	rec, err := srv.store.Load(testSandboxID)
	if err != nil || !rec.Stopped {
		t.Errorf("record after stop: stopped=%v err=%v", rec.Stopped, err)
	}
	// The seed dir and the VM itself survive: start needs both.
	if _, err := fake.Get(ctx, name); err != nil {
		t.Error("stop must not delete the VM")
	}

	// Polling a stopped sandbox never turns it into an unexpected exit, and
	// never emits the console-tail Warning.
	fake.SetLogs(name, "some console noise")
	srv.pollOnce(ctx)
	srv.mu.Lock()
	cond = srv.sandboxes[testSandboxID].cond
	srv.mu.Unlock()
	if cond.Reason != reasonContainerStopped {
		t.Errorf("poll rewrote a stopped sandbox to %+v", cond)
	}
	for len(events) > 0 {
		ev := <-events
		if pe := ev.GetPlatformEvent(); pe != nil && pe.GetEvent().GetType() == "Warning" {
			t.Errorf("unexpected Warning event for an intentional stop: %s", pe.GetEvent().GetMessage())
		}
	}

	// Idempotent: stopping again is a no-op success.
	if _, err := client.StopSandbox(ctx, &computev1.StopSandboxRequest{SandboxId: testSandboxID}); err != nil {
		t.Errorf("second stop: %v", err)
	}

	// Start brings it back; the flag clears and the condition is Ready.
	if _, err := client.StartSandbox(ctx, &computev1.StartSandboxRequest{SandboxName: "sb-" + testSandboxID[:8]}); err != nil {
		t.Fatalf("start (by name): %v", err)
	}
	if got := fake.Starts(); len(got) != 1 || got[0] != name {
		t.Errorf("start calls = %v", got)
	}
	waitForCondition(t, srv, reasonBackendReady)
	rec, _ = srv.store.Load(testSandboxID)
	if rec.Stopped {
		t.Error("stopped flag not cleared by start")
	}
	if _, err := client.StartSandbox(ctx, &computev1.StartSandboxRequest{SandboxId: testSandboxID}); err != nil {
		t.Errorf("second start: %v", err)
	}
}

func TestStopSandboxErrors(t *testing.T) {
	fake := &backend.Fake{}
	srv, client := readySandbox(t, fake)
	ctx := context.Background()

	if _, err := client.StopSandbox(ctx, &computev1.StopSandboxRequest{SandboxId: "0195c1a2-9999-9999-9999-999999999999"}); status.Code(err) != codes.NotFound {
		t.Errorf("unknown sandbox: want NotFound, got %v", err)
	}

	// Runtime failure with the VM still running is a real error, and the
	// sandbox must not be marked stopped.
	fake.StopError = errors.New("apiserver busy")
	if _, err := client.StopSandbox(ctx, &computev1.StopSandboxRequest{SandboxId: testSandboxID}); status.Code(err) != codes.Internal {
		t.Errorf("runtime failure: want Internal, got %v", err)
	}
	rec, _ := srv.store.Load(testSandboxID)
	if rec.Stopped {
		t.Error("failed stop must not persist stopped=true")
	}
	srv.mu.Lock()
	busy := srv.sandboxes[testSandboxID].busy
	srv.mu.Unlock()
	if busy {
		t.Error("busy mark leaked after a failed stop")
	}

	// A VM that vanished underneath us is NotFound.
	if err := fake.Delete(ctx, "oshl-"+testSandboxID); err != nil {
		t.Fatal(err)
	}
	if _, err := client.StopSandbox(ctx, &computev1.StopSandboxRequest{SandboxId: testSandboxID}); status.Code(err) != codes.NotFound {
		t.Errorf("missing VM: want NotFound, got %v", err)
	}
}

func TestStartSandboxErrors(t *testing.T) {
	fake := &backend.Fake{}
	srv, client := readySandbox(t, fake)
	ctx := context.Background()
	if _, err := client.StopSandbox(ctx, &computev1.StopSandboxRequest{SandboxId: testSandboxID}); err != nil {
		t.Fatal(err)
	}
	fake.StartError = errors.New("boot failed")
	if _, err := client.StartSandbox(ctx, &computev1.StartSandboxRequest{SandboxId: testSandboxID}); status.Code(err) != codes.Internal {
		t.Errorf("runtime failure: want Internal, got %v", err)
	}
	// Still stopped, still recoverable.
	rec, _ := srv.store.Load(testSandboxID)
	if !rec.Stopped {
		t.Error("failed start must keep stopped=true")
	}
	if _, err := client.StartSandbox(ctx, &computev1.StartSandboxRequest{SandboxId: testSandboxID}); err != nil {
		t.Errorf("retry after failure: %v", err)
	}
}

func TestLifecycleRefusesProvisioningAndDeleting(t *testing.T) {
	fake := &backend.Fake{RunBlock: make(chan struct{})}
	srv := newLiveServer(t, fake)
	client := dialTestServer(t, srv)
	ctx := context.Background()
	if _, err := client.CreateSandbox(ctx, createRequest()); err != nil {
		t.Fatal(err)
	}
	// Still provisioning (blocked in the extraction Run).
	if _, err := client.StopSandbox(ctx, &computev1.StopSandboxRequest{SandboxId: testSandboxID}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("stop during provisioning: want FailedPrecondition, got %v", err)
	}
	close(fake.RunBlock)
	waitForCondition(t, srv, reasonBackendReady)
	waitForProvisioning(t, srv)

	srv.mu.Lock()
	srv.sandboxes[testSandboxID].deleting = true
	srv.mu.Unlock()
	if _, err := client.StartSandbox(ctx, &computev1.StartSandboxRequest{SandboxId: testSandboxID}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("start during delete: want FailedPrecondition, got %v", err)
	}
}

func TestBootstrapHonorsStoppedRecord(t *testing.T) {
	fake := &backend.Fake{}
	srv := newLiveServer(t, fake)
	id := "0195c1a2-eeee-0000-0000-000000000005"
	seedRecord(t, srv, id, "oshl-"+id)
	rec, _ := srv.store.Load(id)
	rec.Stopped = true
	if err := srv.store.Save(rec); err != nil {
		t.Fatal(err)
	}
	if _, err := fake.Run(context.Background(), backend.RunSpec{
		Name: "oshl-" + id, Image: testImage,
		Labels: map[string]string{labelManagedBy: managedByValue, labelSandboxID: id},
	}); err != nil {
		t.Fatal(err)
	}
	fake.SetState("oshl-"+id, "stopped")
	fake.SetLogs("oshl-"+id, "this is not a failure")

	if err := srv.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	cond := srv.sandboxes[id].cond
	srv.mu.Unlock()
	if cond.Reason != reasonContainerStopped || cond.Message != "Sandbox VM is stopped" {
		t.Errorf("adopted stopped sandbox condition = %+v", cond)
	}
}

func TestPollerClearsStoppedWhenStartedOutOfBand(t *testing.T) {
	fake := &backend.Fake{}
	srv, client := readySandbox(t, fake)
	ctx := context.Background()
	if _, err := client.StopSandbox(ctx, &computev1.StopSandboxRequest{SandboxId: testSandboxID}); err != nil {
		t.Fatal(err)
	}
	// `container start` behind the gateway's back: the runtime is the truth.
	fake.SetState("oshl-"+testSandboxID, "running")
	srv.pollOnce(ctx)
	waitForCondition(t, srv, reasonBackendReady)
	rec, _ := srv.store.Load(testSandboxID)
	if rec.Stopped {
		t.Error("out-of-band start must clear the persisted stopped flag")
	}
	// And a later out-of-band exit is again an unexpected one.
	fake.SetState("oshl-"+testSandboxID, "stopped")
	srv.pollOnce(ctx)
	waitForCondition(t, srv, reasonContainerExited)
}

// countingRuntime counts List calls so the idle-skip is observable.
type countingRuntime struct {
	*backend.Fake
	lists int
}

func (c *countingRuntime) List(ctx context.Context, all bool) ([]backend.Container, error) {
	c.lists++
	return c.Fake.List(ctx, all)
}

func TestPollerSkipsRuntimeWhenIdle(t *testing.T) {
	rt := &countingRuntime{Fake: &backend.Fake{}}
	srv := newTestServer(t)
	srv.rt = rt
	srv.pollOnce(context.Background())
	if rt.lists != 0 {
		t.Errorf("idle poll hit the runtime %d times; want 0", rt.lists)
	}
}
