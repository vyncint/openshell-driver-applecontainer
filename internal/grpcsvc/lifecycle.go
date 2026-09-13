package grpcsvc

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/vyncint/openshell-driver-applecontainer/internal/backend"
	computev1 "github.com/vyncint/openshell-driver-applecontainer/internal/gen/computev1"
)

// lifecycleTimeout bounds a stop or start once the driver has committed to
// it. Like delete, the operation runs detached from the request context: the
// gateway has already persisted the Stopping/Starting phase, and a half-
// applied stop would leave a VM the poller reports as an unexpected exit.
const lifecycleTimeout = 60 * time.Second

// StopSandbox powers the VM off and keeps everything else — record, seed
// directory, the container itself — so StartSandbox can bring it back with
// the same identity, environment and token. Idempotent: a stopped or
// stopping sandbox returns success.
//
// The gateway drives this from `openshell sandbox stop`. It has already
// moved the sandbox to phase Stopping and completes the transition to
// Stopped when this RPC succeeds (or when the next watch snapshot carries a
// ContainerStopped condition — see conditions.go).
func (s *Server) StopSandbox(ctx context.Context, req *computev1.StopSandboxRequest) (*computev1.StopSandboxResponse, error) {
	s.log.Info("stop sandbox requested", "sandbox_id", req.GetSandboxId(), "name", req.GetSandboxName())
	e, release, err := s.beginLifecycle(req.GetSandboxId(), req.GetSandboxName())
	if err != nil {
		return nil, err
	}
	defer release()

	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lifecycleTimeout)
	defer cancel()

	name := e.rec.ContainerName
	if err := s.rt.Stop(opCtx, name); err != nil {
		if errors.Is(err, backend.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "sandbox %q has no VM to stop", e.rec.ID)
		}
		// apple/container answers a stop of an already-stopped VM with an
		// error on some releases; only trust it after checking the state.
		if c, gerr := s.rt.Get(opCtx, name); gerr != nil || c.State == "running" {
			return nil, status.Errorf(codes.Internal, "stop sandbox VM: %v", err)
		}
	}

	if err := s.setStopped(e, true); err != nil {
		return nil, err
	}
	s.setCondition(e, stoppedCondition())
	s.publishSandbox(e)
	s.publishPlatformEvent(e.rec.ID, "Normal", "Stopped", "Sandbox VM stopped")
	return &computev1.StopSandboxResponse{}, nil
}

// StartSandbox boots a stopped sandbox VM again. apple/container restarts the
// container with its original configuration, so the seed mount, environment
// and token are all still in place and the supervisor dials the gateway back
// exactly as after a fresh create. Idempotent: a running sandbox returns
// success.
func (s *Server) StartSandbox(ctx context.Context, req *computev1.StartSandboxRequest) (*computev1.StartSandboxResponse, error) {
	s.log.Info("start sandbox requested", "sandbox_id", req.GetSandboxId(), "name", req.GetSandboxName())
	e, release, err := s.beginLifecycle(req.GetSandboxId(), req.GetSandboxName())
	if err != nil {
		return nil, err
	}
	defer release()

	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lifecycleTimeout)
	defer cancel()

	name := e.rec.ContainerName
	if err := s.rt.Start(opCtx, name); err != nil {
		if errors.Is(err, backend.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "sandbox %q has no VM to start", e.rec.ID)
		}
		if c, gerr := s.rt.Get(opCtx, name); gerr != nil || c.State != "running" {
			return nil, status.Errorf(codes.Internal, "start sandbox VM: %v", err)
		}
	}

	if err := s.setStopped(e, false); err != nil {
		return nil, err
	}
	s.setCondition(e, readyTrueCondition())
	s.publishSandbox(e)
	s.publishPlatformEvent(e.rec.ID, "Normal", "Started", "Sandbox VM started")
	return &computev1.StartSandboxResponse{}, nil
}

// beginLifecycle resolves the target entry and marks it busy for the
// duration of a stop/start, refusing sandboxes that are still provisioning
// or already being deleted. The returned release func clears the mark.
func (s *Server) beginLifecycle(id, name string) (*entry, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.lookupLocked(id, name)
	if !ok {
		return nil, nil, status.Errorf(codes.NotFound, "sandbox %q not found", id)
	}
	switch {
	case e.deleting:
		return nil, nil, status.Errorf(codes.FailedPrecondition, "sandbox %q is being deleted", e.rec.ID)
	case !e.provisionDone():
		return nil, nil, status.Errorf(codes.FailedPrecondition, "sandbox %q is still provisioning", e.rec.ID)
	case e.busy:
		return nil, nil, status.Errorf(codes.Aborted, "sandbox %q has a lifecycle operation in progress", e.rec.ID)
	case e.cond.Reason == reasonProvisioningFailed:
		return nil, nil, status.Errorf(codes.FailedPrecondition, "sandbox %q failed to provision; delete it instead", e.rec.ID)
	}
	e.busy = true
	release := func() {
		s.mu.Lock()
		e.busy = false
		s.mu.Unlock()
	}
	return e, release, nil
}

// setStopped records the gateway's lifecycle intent on the entry and on
// disk, so a driver restart keeps reporting a stopped VM as stopped.
func (s *Server) setStopped(e *entry, stopped bool) error {
	s.mu.Lock()
	e.rec.Stopped = stopped
	rec := e.rec
	s.mu.Unlock()
	if err := s.store.Save(rec); err != nil {
		return status.Errorf(codes.Internal, "persist lifecycle state: %v", err)
	}
	return nil
}
