package grpcsvc

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/vyncint/openshell-driver-applecontainer/internal/backend"
)

// pollInterval matches the upstream docker driver's watch cadence.
const pollInterval = 2 * time.Second

// Bootstrap reconciles persisted records against the container runtime at
// startup. apple/container VMs are managed by the system apiserver and
// survive a driver restart, so the strategy is adopt/mark-failed/clean:
//
//   - record + running VM   → adopt as Ready
//   - record + stopped VM   → terminal ContainerExited
//   - record + no VM        → terminal ProvisioningFailed
//   - our labeled VM, no record → orphan, deleted
func (s *Server) Bootstrap(ctx context.Context) error {
	records, skipped, err := s.store.List()
	if err != nil {
		return err
	}
	for _, dir := range skipped {
		s.log.Warn("skipping unreadable state entry", "entry", dir)
	}

	containers, err := s.rt.List(ctx, true)
	if err != nil {
		return err
	}
	byName := make(map[string]backend.Container, len(containers))
	for _, c := range containers {
		byName[c.ID] = c
	}

	entries := make([]*entry, 0, len(records))
	for _, rec := range records {
		e := &entry{rec: rec}
		if c, ok := byName[rec.ContainerName]; ok {
			if c.State == "running" {
				e.cond = readyTrueCondition()
			} else {
				// The VM died while the driver was away; attach its
				// console tail so the failure is diagnosable through
				// OpenShell instead of via manual container commands.
				e.cond = exitedCondition()
				if tail := s.consoleTail(ctx, rec.ContainerName); tail != "" {
					e.cond.Message += "; console tail:\n" + tail
				}
			}
		} else {
			e.cond = failedCondition("sandbox VM missing after driver restart")
		}
		entries = append(entries, e)
	}

	s.mu.Lock()
	for _, e := range entries {
		s.sandboxes[e.rec.ID] = e
		s.log.Info("reconciled sandbox record",
			"sandbox_id", e.rec.ID, "container", e.rec.ContainerName,
			"status", e.cond.Status, "reason", e.cond.Reason)
	}
	known := make(map[string]bool, len(s.sandboxes))
	for id := range s.sandboxes {
		known[id] = true
	}
	s.mu.Unlock()

	// Orphans: containers we manage without a surviving record.
	for _, c := range containers {
		if c.Labels[labelManagedBy] != managedByValue {
			continue
		}
		id := c.Labels[labelSandboxID]
		if id != "" && known[id] {
			continue
		}
		s.log.Warn("deleting orphaned sandbox VM", "container", c.ID, "sandbox_id", id)
		if err := s.rt.Delete(ctx, c.ID); err != nil && !errors.Is(err, backend.ErrNotFound) {
			s.log.Error("failed to delete orphaned VM", "container", c.ID, "err", err)
		}
	}
	return nil
}

// StartPoller launches the runtime poller; the runtime, not the driver's
// records, is the source of truth for sandbox liveness.
func (s *Server) StartPoller() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-s.bgCtx.Done():
				return
			case <-ticker.C:
				s.pollOnce(s.bgCtx)
			}
		}
	}()
}

// pollOnce diffs runtime state into conditions and publishes transitions.
func (s *Server) pollOnce(ctx context.Context) {
	containers, err := s.rt.List(ctx, true)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("runtime poll failed", "err", err)
		}
		return
	}
	byName := make(map[string]backend.Container, len(containers))
	for _, c := range containers {
		byName[c.ID] = c
	}

	var changed []*entry
	var exited []*entry
	s.mu.Lock()
	for _, e := range s.sandboxes {
		if e.deleting || !e.provisionDone() {
			continue
		}
		var next condition
		if c, ok := byName[e.rec.ContainerName]; ok {
			if c.State == "running" {
				next = readyTrueCondition()
			} else {
				next = exitedCondition()
			}
		} else {
			if e.cond.Reason == reasonProvisioningFailed {
				continue // keep the original failure message
			}
			next = failedCondition("sandbox VM disappeared from the runtime")
		}
		if next.Status != e.cond.Status || next.Reason != e.cond.Reason {
			e.cond = next
			changed = append(changed, e)
			if next.Reason == reasonContainerExited {
				exited = append(exited, e)
			}
		}
	}
	s.mu.Unlock()

	// Fetched only on the transition, never on steady-state polls, and a
	// logs failure never blocks the transition itself.
	for _, e := range exited {
		tail := s.consoleTail(ctx, e.rec.ContainerName)
		if tail == "" {
			continue
		}
		// The entry may have moved on while logs were being fetched (e.g.
		// a concurrent delete); both the message and the Warning event are
		// gated on it still being exited so stale warnings are never
		// emitted.
		s.mu.Lock()
		stillExited := e.cond.Reason == reasonContainerExited && !e.deleting
		if stillExited {
			e.cond.Message += "; console tail:\n" + tail
		}
		s.mu.Unlock()
		if stillExited {
			s.publishPlatformEvent(e.rec.ID, "Warning", reasonContainerExited,
				"Sandbox VM exited; console tail:\n"+tail)
		}
	}

	for _, e := range changed {
		s.log.Info("sandbox state transition",
			"sandbox_id", e.rec.ID, "status", e.cond.Status, "reason", e.cond.Reason)
		s.publishSandbox(e)
	}
}

// consoleTailLines and consoleTailMaxRunes bound how much guest console
// output is attached to conditions and events.
const (
	consoleTailLines    = 20
	consoleTailMaxRunes = 700
)

// consoleTail returns a bounded excerpt of a container's console output,
// or "" when logs are unavailable.
func (s *Server) consoleTail(ctx context.Context, name string) string {
	out, err := s.rt.Logs(ctx, name, consoleTailLines)
	if err != nil {
		s.log.Debug("console tail unavailable", "container", name, "err", err)
		return ""
	}
	out = strings.TrimSpace(out)
	if runes := []rune(out); len(runes) > consoleTailMaxRunes {
		out = "…" + string(runes[len(runes)-consoleTailMaxRunes:])
	}
	return out
}

// provisionDone reports whether the provisioning task (if any) finished.
func (e *entry) provisionDone() bool {
	if e.done == nil {
		return true
	}
	select {
	case <-e.done:
		return true
	default:
		return false
	}
}
