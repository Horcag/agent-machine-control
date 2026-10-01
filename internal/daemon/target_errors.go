package daemon

import (
	"context"
	"errors"
	"net/http"

	"github.com/Horcag/agent-machine-control/internal/backends/hyperv"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/target"
)

type targetFailure struct {
	status            int
	category, message string
	causes            []error
}

// Only typed allowlisted causes become public protocol metadata, never provider text.
func classifyTargetFailure(err error) (targetFailure, bool) {
	classes := []targetFailure{
		{http.StatusConflict, "target_not_enrolled", "no target is enrolled; enroll a local target first", []error{target.ErrNoDefault}},
		{http.StatusBadRequest, "target_mismatch", "target reference does not identify the enrolled target", []error{target.ErrDifferentTarget}},
		{http.StatusRequestTimeout, "cancelled", "target observation was cancelled", []error{context.Canceled}},
		{http.StatusGatewayTimeout, "timeout", "target observation deadline exceeded", []error{context.DeadlineExceeded, hyperv.ErrCommandTimeout}},
		{http.StatusServiceUnavailable, "host_unavailable", "backend management host is unavailable", []error{hyperv.ErrHostUnavailable, domain.ErrMachineHostUnavailable, domain.ErrMachineHostDisabled}},
		{http.StatusForbidden, "access_denied", "machine backend access denied", []error{hyperv.ErrAccessDenied, domain.ErrMachineAccessDenied}},
		{http.StatusServiceUnavailable, "module_missing", "Hyper-V PowerShell module is unavailable", []error{hyperv.ErrModuleMissing}},
		{http.StatusServiceUnavailable, "backend_unavailable", "machine backend is unavailable", []error{hyperv.ErrExecutableNotFound, hyperv.ErrBackendUnavailable}},
		{http.StatusNotFound, "machine_not_found", "enrolled machine was not found", []error{hyperv.ErrMachineNotFound}},
		{http.StatusBadGateway, "malformed_response", "machine backend returned an invalid response", []error{hyperv.ErrMalformedResponse, hyperv.ErrUnexpectedSchemaVersion, hyperv.ErrDuplicateMachineID, hyperv.ErrTrailingData, hyperv.ErrOutputExceededLimit}},
		{http.StatusServiceUnavailable, "target_unavailable", "enrolled target inventory is unavailable", []error{target.ErrInventoryRefresh}},
		{http.StatusBadRequest, "invalid_target", "target reference is invalid or stale", []error{domain.ErrInvalidMachineLocator, domain.ErrMachineReferenceMiss, domain.ErrMachineReferenceStale, target.ErrUnsupportedHost}},
	}
	for _, class := range classes {
		for _, cause := range class.causes {
			if errors.Is(err, cause) {
				return class, true
			}
		}
	}
	return targetFailure{}, false
}

func isTargetResolutionFailure(err error) bool {
	_, known := classifyTargetFailure(err)
	return known
}

func writeTargetResolutionError(w http.ResponseWriter, err error) {
	if class, known := classifyTargetFailure(err); known {
		writeError(w, class.status, class.category, class.message)
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_target", "target reference is invalid or stale")
}
