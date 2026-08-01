package grpcsvc

import (
	"time"

	computev1 "github.com/vyncint/openshell-driver-applecontainer/internal/gen/computev1"
)

// Condition reasons. The gateway derives the public phase from the first
// condition with type "Ready": status True → Ready; status False → Error
// unless the reason is in its transient set (lowercased "starting" is; the
// others here are terminal by design). See docs/CONTRACT.md §4.
const (
	conditionReady = "Ready"

	reasonStarting           = "Starting"
	reasonBackendReady       = "BackendReady"
	reasonContainerExited    = "ContainerExited"
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

func failedCondition(msg string) condition {
	return condition{Status: "False", Reason: reasonProvisioningFailed, Message: msg, At: time.Now()}
}

func deletingCondition() condition {
	return condition{Status: "False", Reason: reasonDeleting, Message: "Sandbox is being deleted", At: time.Now()}
}
