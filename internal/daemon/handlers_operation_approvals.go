package daemon

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/target"
)

func (s *Server) handleIssueOperationApproval(w http.ResponseWriter, r *http.Request) {
	caller, ok := getCallerContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthenticated caller")
		return
	}
	if caller.IsDelegated() || !caller.HasScope(domain.ScopeOperationAdmin) {
		writeError(w, http.StatusForbidden, "forbidden", "operation approval issuance requires operator operation:admin authority")
		return
	}
	contentType := r.Header.Get("Content-Type")
	if contentType != "" && !strings.HasPrefix(strings.ToLower(contentType), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "invalid_argument", "Content-Type must be application/json")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var request OperationApprovalIssueRequest
	if err := decodeStrictJSONObject(r.Body, &request); err != nil || validateOperationApprovalIssueRequest(request) != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid operation approval request")
		return
	}

	grant, _, err := s.recoveryService.IssueOperationApproval(r.Context(), app.OperationApprovalIssueParams{
		Kind: domain.OperationKind(request.Kind), Caller: caller, Target: request.Target,
		Reason: request.Reason, IdempotencyKey: request.IdempotencyKey,
		ValidFor:    time.Duration(request.ValidForMillis) * time.Millisecond,
		Beneficiary: request.Beneficiary, Parameters: request.Parameters,
	})
	if err != nil {
		writeOperationApprovalIssueError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, OperationApprovalIssueResponse{
		SchemaVersion: SchemaVersion, ApprovalID: grant.ApprovalID,
		Deadline: grant.Deadline.UTC().Format(time.RFC3339Nano), ExpiresAt: grant.ExpiresAt.UTC().Format(time.RFC3339Nano),
		Operation: OperationApprovalOperationDTO{
			Kind: string(grant.Operation.Kind), Target: string(grant.Operation.Target), Reason: grant.Operation.Reason,
			IdempotencyKey: grant.Operation.IdempotencyKey, Parameters: grant.Operation.Parameters,
		},
	})
}

func validateOperationApprovalIssueRequest(request OperationApprovalIssueRequest) error {
	if err := domain.ValidateReason(request.Reason); err != nil {
		return err
	}
	if err := domain.ValidateIdempotencyKey(request.IdempotencyKey); err != nil {
		return err
	}
	if request.ValidForMillis < int64(time.Second/time.Millisecond) || request.ValidForMillis > int64((5*time.Minute)/time.Millisecond) {
		return domain.ErrMissingDeadline
	}
	if request.Beneficiary != "" && request.Beneficiary != "self" && request.Beneficiary != "agent:mcp-local" {
		return app.ErrOperationApprovalForbidden
	}
	if err := domain.ValidateOperationParameters(domain.OperationKind(request.Kind), request.Parameters); err != nil {
		return err
	}
	switch request.Kind {
	case "machine.start", "machine.stop", "checkpoint.create", "checkpoint.restore":
		return domain.MachineRef(request.Target).Validate()
	default:
		return domain.ErrInvalidOperationKind
	}
}

func writeOperationApprovalIssueError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, app.ErrOperationApprovalForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "operation approval issuance is forbidden")
	case errors.Is(err, app.ErrOperationApprovalNotRequired):
		writeError(w, http.StatusConflict, "approval_not_required", "exact current operation does not require approval")
	case errors.Is(err, target.ErrNoDefault), errors.Is(err, target.ErrDifferentTarget), errors.Is(err, target.ErrInventoryRefresh),
		errors.Is(err, domain.ErrMachineHostUnavailable), errors.Is(err, domain.ErrMachineAccessDenied):
		writeTargetResolutionError(w, err)
	default:
		var denied *app.PolicyDeniedError
		if errors.As(err, &denied) {
			writeError(w, http.StatusForbidden, string(denied.Reason), denied.Message)
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_argument", "operation approval could not be issued")
	}
}
