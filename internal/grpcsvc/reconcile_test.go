package grpcsvc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/vyncint/openshell-driver-applecontainer/internal/backend"
	"github.com/vyncint/openshell-driver-applecontainer/internal/state"
)

// seedRecord persists a record as if a previous driver instance accepted it.
func seedRecord(t *testing.T, srv *Server, id, containerName string) {
	t.Helper()
	err := srv.store.Save(state.Record{
		ID:            id,
		Name:          "sb-" + id[:6],
		ContainerName: containerName,
		ImageRef:      testImage,
		CreatedAt:     time.Now().UTC(),
		Sandbox:       json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapAdoptsRunningVM(t *testing.T) {
	fake := &backend.Fake{}
	srv := newLiveServer(t, fake)
	id := "0195c1a2-aaaa-0000-0000-000000000001"
	seedRecord(t, srv, id, "oshl-"+id)
	if _, err := fake.Run(context.Background(), backend.RunSpec{
		Name: "oshl-" + id, Image: testImage,
		Labels: map[string]string{labelManagedBy: managedByValue, labelSandboxID: id},
	}); err != nil {
		t.Fatal(err)
	}

	if err := srv.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	e := srv.sandboxes[id]
	srv.mu.Unlock()
	if e == nil {
		t.Fatal("record not adopted")
	}
	if e.cond.Status != "True" || e.cond.Reason != reasonBackendReady {
		t.Errorf("adopted condition = %+v", e.cond)
	}
}

func TestBootstrapMarksStoppedAndMissing(t *testing.T) {
	fake := &backend.Fake{}
	srv := newLiveServer(t, fake)

	stopped := "0195c1a2-bbbb-0000-0000-000000000002"
	seedRecord(t, srv, stopped, "oshl-"+stopped)
	if _, err := fake.Run(context.Background(), backend.RunSpec{
		Name: "oshl-" + stopped, Image: testImage,
		Labels: map[string]string{labelManagedBy: managedByValue, labelSandboxID: stopped},
	}); err != nil {
		t.Fatal(err)
	}
	fake.SetState("oshl-"+stopped, "stopped")
	fake.SetLogs("oshl-"+stopped, "mkdir: cannot create directory: Permission denied")

	missing := "0195c1a2-cccc-0000-0000-000000000003"
	seedRecord(t, srv, missing, "oshl-"+missing)

	if err := srv.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	condStopped := srv.sandboxes[stopped].cond
	condMissing := srv.sandboxes[missing].cond
	srv.mu.Unlock()
	if condStopped.Reason != reasonContainerExited {
		t.Errorf("stopped VM condition = %+v", condStopped)
	}
	if !strings.Contains(condStopped.Message, "Permission denied") {
		t.Errorf("stopped VM condition lacks console tail: %q", condStopped.Message)
	}
	if condMissing.Reason != reasonProvisioningFailed {
		t.Errorf("missing VM condition = %+v", condMissing)
	}
}

func TestBootstrapDeletesOrphans(t *testing.T) {
	fake := &backend.Fake{}
	srv := newLiveServer(t, fake)

	// Our label, no record: orphan.
	if _, err := fake.Run(context.Background(), backend.RunSpec{
		Name: "oshl-orphan", Image: testImage,
		Labels: map[string]string{labelManagedBy: managedByValue, labelSandboxID: "0195c1a2-dddd-0000-0000-000000000004"},
	}); err != nil {
		t.Fatal(err)
	}
	// Foreign container: untouched.
	if _, err := fake.Run(context.Background(), backend.RunSpec{Name: "unrelated", Image: "other:1"}); err != nil {
		t.Fatal(err)
	}

	if err := srv.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := fake.Get(context.Background(), "oshl-orphan"); err == nil {
		t.Error("orphan VM was not deleted")
	}
	if _, err := fake.Get(context.Background(), "unrelated"); err != nil {
		t.Error("foreign container must not be touched")
	}
}

func TestPollerTracksExit(t *testing.T) {
	fake := &backend.Fake{}
	srv := newLiveServer(t, fake)
	client := dialTestServer(t, srv)

	if _, err := client.CreateSandbox(context.Background(), createRequest()); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, srv, reasonBackendReady)

	// Subscribe before the transition to capture the Warning event.
	subID, events := srv.hub.subscribe()
	defer srv.hub.unsubscribe(subID)

	// VM dies out-of-band; the poller must notice, flip the condition, and
	// attach the guest console tail for diagnosability.
	fake.SetState("oshl-"+testSandboxID, "stopped")
	fake.SetLogs("oshl-"+testSandboxID, "boot.sh: fatal: something exploded\n")
	srv.pollOnce(context.Background())

	cond := waitForCondition(t, srv, reasonContainerExited)
	if cond.Status != "False" {
		t.Errorf("condition = %+v", cond)
	}
	if !strings.Contains(cond.Message, "something exploded") {
		t.Errorf("condition message lacks console tail: %q", cond.Message)
	}

	var sawWarning bool
	for len(events) > 0 {
		ev := <-events
		if pe := ev.GetPlatformEvent(); pe != nil &&
			pe.GetEvent().GetType() == "Warning" &&
			strings.Contains(pe.GetEvent().GetMessage(), "something exploded") {
			sawWarning = true
		}
	}
	if !sawWarning {
		t.Error("no Warning platform event carrying the console tail")
	}

	// And back: runtime says running again (e.g. container start).
	fake.SetState("oshl-"+testSandboxID, "running")
	srv.pollOnce(context.Background())
	waitForCondition(t, srv, reasonBackendReady)
}

func TestConsoleTailTruncatesAndSurvivesErrors(t *testing.T) {
	fake := &backend.Fake{}
	srv := newLiveServer(t, fake)

	// Unknown container: logs error must yield "" and not propagate.
	if got := srv.consoleTail(context.Background(), "no-such"); got != "" {
		t.Errorf("tail for unknown container = %q", got)
	}

	if _, err := fake.Run(context.Background(), backend.RunSpec{Name: "big", Image: testImage}); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("x", 2000) + "END"
	fake.SetLogs("big", long)
	got := srv.consoleTail(context.Background(), "big")
	if len([]rune(got)) > consoleTailMaxRunes+1 { // +1 for the ellipsis
		t.Errorf("tail not truncated: %d runes", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "END") {
		t.Errorf("truncation must keep the end of the output, got %q…", got[:20])
	}
}

func TestPollerSkipsInFlightProvisioning(t *testing.T) {
	fake := &backend.Fake{RunBlock: make(chan struct{})}
	srv := newLiveServer(t, fake)
	client := dialTestServer(t, srv)

	if _, err := client.CreateSandbox(context.Background(), createRequest()); err != nil {
		t.Fatal(err)
	}
	// Provisioning is blocked; no container exists yet. A poll must NOT
	// mark the sandbox failed.
	srv.pollOnce(context.Background())
	srv.mu.Lock()
	cond := srv.sandboxes[testSandboxID].cond
	srv.mu.Unlock()
	if cond.Reason != reasonStarting {
		t.Errorf("in-flight sandbox condition = %+v, want Starting", cond)
	}
	close(fake.RunBlock)
	waitForCondition(t, srv, reasonBackendReady)
}

func TestPollerKeepsProvisioningFailure(t *testing.T) {
	fake := &backend.Fake{}
	srv := newLiveServer(t, fake)
	client := dialTestServer(t, srv)

	if _, err := srv.extractor.Ensure(context.Background(), testSupImage); err != nil {
		t.Fatal(err)
	}
	fake.RunError = context.DeadlineExceeded
	if _, err := client.CreateSandbox(context.Background(), createRequest()); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, srv, reasonProvisioningFailed)

	// The VM never existed; polling must keep the original failure.
	srv.pollOnce(context.Background())
	srv.mu.Lock()
	cond := srv.sandboxes[testSandboxID].cond
	srv.mu.Unlock()
	if cond.Reason != reasonProvisioningFailed {
		t.Errorf("condition rewritten to %+v", cond)
	}
}
