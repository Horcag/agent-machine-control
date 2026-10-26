package daemon

import (
	"context"
	"errors"
	"net/http"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
	"github.com/Horcag/agent-machine-control/internal/policy"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

// Guest failures carry allowlisted categories and fixed messages, never provider text.
// Uncertainty takes precedence over joined causes: a dispatched effect must not
// be presented as a safe pre-dispatch rejection.
func guestFailureCategory(err error) string {
	if errors.Is(err, app.ErrGuestUncertain) {
		return "guest_uncertain"
	}
	if denied, ok := errors.AsType[*app.PolicyDeniedError](err); ok {
		switch denied.Reason {
		case policy.DenialApprovalRequired, policy.DenialApprovalExpired, policy.DenialApprovalConsumed,
			policy.DenialApprovalMismatch, policy.DenialApprovalNotYetValid, policy.DenialMissingScope,
			policy.DenialMissingSensitiveEvidenceScope, policy.DenialRollbackMissing, policy.DenialForbidden,
			policy.DenialDelegationExceeded, policy.DenialMissingCapability:
			return string(denied.Reason)
		default:
			return "forbidden"
		}
	}
	classes := []struct {
		category string
		causes   []error
	}{
		{"guest_workspace_unprovisioned", []error{app.ErrGuestWorkspaceUnprovisioned}},
		{"guest_authority", []error{app.ErrGuestAuthority}},
		{"guest_not_prepared", []error{app.ErrGuestNotPrepared}},
		{"guest_ownership", []error{app.ErrGuestOwnership}},
		{"invalid_argument", []error{domain.ErrNonCanonicalParameter, domain.ErrInvalidOperationKind, domain.ErrMissingDeadline, domain.ErrMissingReason, domain.ErrInvalidReason, domain.ErrMissingIdempotencyKey, domain.ErrInvalidIdempotencyKey, stream.ErrRequest}},
		{"forbidden", []error{domain.ErrSessionAccessDenied, domain.ErrDelegationExceedsAuthority}},
		{"conflict", []error{domain.ErrSessionConflict, receipt.ErrIdempotencyCollision}},
		{"timeout", []error{context.DeadlineExceeded}},
		{"cancelled", []error{context.Canceled}},
	}
	for _, class := range classes {
		for _, cause := range class.causes {
			if errors.Is(err, cause) {
				return class.category
			}
		}
	}
	if class, known := classifyTargetFailure(err); known {
		return class.category
	}
	return "guest_uncertain"
}

// Only legacy clients need the early HTTP error path. A started stream or an
// execution receipt cannot be rewritten as a rejection: its outcome stays in
// the final trailer, and the client must verify it before taking another action.
func writeGuestEarlyError(w http.ResponseWriter, category string, result app.GuestOperationResult, started bool) bool {
	if started || category == "guest_uncertain" || (result.Receipt != nil && result.Receipt.Outcome.Status != domain.OutcomeDenied) {
		return false
	}
	status := http.StatusForbidden
	switch category {
	case "invalid_argument", "invalid_target", "target_mismatch":
		status = http.StatusBadRequest
	case "guest_not_prepared", "guest_ownership", "conflict", "target_not_enrolled":
		status = http.StatusConflict
	case "timeout":
		status = http.StatusGatewayTimeout
	case "cancelled":
		status = http.StatusRequestTimeout
	case "target_unavailable", "host_unavailable", "module_missing", "backend_unavailable":
		status = http.StatusServiceUnavailable
	case "machine_not_found":
		status = http.StatusNotFound
	}
	w.Header().Del("Trailer")
	w.Header().Del(GuestResultTrailer)
	writeError(w, status, category, "guest operation rejected before dispatch; inspect its category")
	return true
}
