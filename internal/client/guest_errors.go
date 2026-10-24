package client

import (
	"errors"
	"net/http"

	"github.com/Horcag/agent-machine-control/internal/app"
)

// Ignore trailer message text even from older servers. Categories are a bounded
// protocol vocabulary; unknown or legacy failures cannot prove a safe outcome.
func guestFinalError(category string) error {
	status := http.StatusForbidden
	var cause error
	message := "guest operation denied"
	switch category {
	case "invalid_argument", "invalid_target", "target_mismatch":
		status = http.StatusBadRequest
		cause = ErrInvalidArgument
		message = "invalid guest operation input"
	case "approval_required", "approval_record_expired", "approval_record_consumed", "approval_record_mismatch", "approval_record_not_yet_valid",
		"forbidden", "forbidden_operation", "missing_required_scope", "missing_sensitive_evidence_scope", "missing_required_capability", "missing_verified_rollback_point", "delegation_exceeds_authority", "access_denied":
		cause = ErrDenied
	case "guest_workspace_unprovisioned":
		cause = errors.Join(app.ErrGuestWorkspaceUnprovisioned, ErrDenied)
		message = "guest workspace requires explicit provisioning"
	case "guest_authority":
		cause = errors.Join(app.ErrGuestAuthority, ErrDenied)
		message = "guest authority changed or unavailable"
	case "guest_not_prepared":
		status = http.StatusConflict
		cause = errors.Join(app.ErrGuestNotPrepared, ErrConflict)
		message = "guest worker requires explicit preparation"
	case "guest_ownership":
		status = http.StatusConflict
		cause = errors.Join(app.ErrGuestOwnership, ErrConflict)
		message = "guest artifact ownership or identity mismatch"
	case "conflict", "target_not_enrolled":
		status = http.StatusConflict
		cause = ErrConflict
		message = "guest operation conflicts with current state"
	case "timeout":
		status = http.StatusGatewayTimeout
		cause = ErrTimeout
		message = "guest operation deadline exceeded"
	case "cancelled":
		status = http.StatusRequestTimeout
		message = "guest operation canceled"
	case "target_unavailable", "host_unavailable", "module_missing", "backend_unavailable":
		status = http.StatusServiceUnavailable
		cause = ErrDaemonUnavailable
		message = "guest target unavailable"
	case "machine_not_found":
		status = http.StatusNotFound
		cause = ErrNotFound
		message = "guest target not found"
	default:
		return app.ErrGuestUncertain
	}
	return errors.Join(cause, &APIError{StatusCode: status, Category: category, Message: message})
}
