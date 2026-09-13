package grpcsvc

import (
	"time"

	computev1 "github.com/vyncint/openshell-driver-applecontainer/internal/gen/computev1"
)

// Condition reasons. The gateway derives the public phase from the first
// condition with type "Ready": status True → Ready; status False → Error
// unless the reason is in its transient set (lowercased "starting" is; the
// others here are terminal by design). See docs/CONTRACT.md §4.
//
// ContainerStopped is special: it is terminal for derive_phase, but the
// gateway consults it by name (`driver_snapshot_confirms_stopped`) to
// complete a StopSandbox transition, and a sandbox already in phase Stopped
// keeps that phase whatever the driver reports. So an intentional stop reads
// as Stopped in `openshell sandbox list`, never as Error.
const (
	conditionReady = "Ready"

	reasonStarting           = "Starting"
	reasonBackendReady       = "BackendReady"
	reasonContainerExited    = "ContainerExited"
	reasonContainerStopped   = "ContainerStopped"
	reasonProvisioningFailed = "ProvisioningFailed"
	reasonDeleting           = "Deleting"
)

type condition struct {
	Status  string // "True" | "False"
	Reason  string
	Message string
	At      time.Time
}

func (c condition) proto() *computev1.DriverCondition {
	lt := ""
	if !c.At.IsZero() {
		lt = c.At.UTC().Format(time.RFC3339)
	}
	return &computev1.DriverCondition{
		Type:               conditionReady,
		Status:             c.Status,
		Reason:             c.Reason,
		Message:            c.Message,
		LastTransitionTime: lt,
	}
}

func startingCondition() condition {
	return condition{Status: "False", Reason: reasonStarting, Message: "Sandbox VM is starting", At: time.Now()}
}

func readyTrueCondition() condition {
	return condition{Status: "True", Reason: reasonBackendReady, Message: "Sandbox VM is running", At: time.Now()}
}

func exitedCondition() condition {
	return condition{Status: "False", Reason: reasonContainerExited, Message: "Sandbox VM is not running", At: time.Now()}
}

func stoppedCondition() condition {
	return condition{Status: "False", Reason: reasonContainerStopped, Message: "Sandbox VM is stopped", At: time.Now()}
}

func failedCondition(msg string) condition {
	return condition{Status: "False", Reason: reasonProvisioningFailed, Message: msg, At: time.Now()}
}

func deletingCondition() condition {
	return condition{Status: "False", Reason: reasonDeleting, Message: "Sandbox is being deleted", At: time.Now()}
}
