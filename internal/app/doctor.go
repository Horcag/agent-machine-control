package app

import (
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

// DoctorStatus defines the operational readiness status of the machine control provider.
type DoctorStatus string

const (
	DoctorReady       DoctorStatus = "ready"
	DoctorUnavailable DoctorStatus = "unavailable"
)

// DoctorReason defines structured diagnostic codes for unavailable states.
type DoctorReason string

const (
	DoctorReasonNone              DoctorReason = ""
	DoctorReasonExecutableMissing DoctorReason = "executable_missing"
	DoctorReasonModuleMissing     DoctorReason = "module_missing"
	DoctorReasonHostUnavailable   DoctorReason = "host_unavailable"
	DoctorReasonAccessDenied      DoctorReason = "access_denied"
	DoctorReasonMalformedOutput   DoctorReason = "malformed_output"
)

// DoctorReport contains the diagnostic evaluation of the machine provider and host environment.
type DoctorReport struct {
	Status       DoctorStatus
	Ready        bool
	Reason       DoctorReason
	Message      string
	Capabilities domain.CapabilitySet
	ObservedAt   time.Time
}

// NewReadyReport creates a successful DoctorReport for a ready host.
func NewReadyReport(caps domain.CapabilitySet, now time.Time) DoctorReport {
	if caps == nil {
		caps = domain.ReadOnlyMachineCapabilities()
	}
	return DoctorReport{
		Status:       DoctorReady,
		Ready:        true,
		Reason:       DoctorReasonNone,
		Message:      "Hyper-V host is ready and accessible",
		Capabilities: caps,
		ObservedAt:   now.UTC(),
	}
}

// NewUnavailableReport creates a DoctorReport detailing why the provider is unavailable.
func NewUnavailableReport(reason DoctorReason, message string, now time.Time) DoctorReport {
	return DoctorReport{
		Status:       DoctorUnavailable,
		Ready:        false,
		Reason:       reason,
		Message:      message,
		Capabilities: domain.NewCapabilitySet(),
		ObservedAt:   now.UTC(),
	}
}
