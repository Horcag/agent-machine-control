package daemon

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/target"
)

const maxTargetRequestBytes = 32 * 1024

func (s *Server) dispatchTargetApproval(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		s.handleIssueTargetApproval(w, r)
		return
	}
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func (s *Server) dispatchTarget(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleGetTarget(w, r)
	case http.MethodPut:
		s.handleMutateTarget(w, r, "target.enroll")
	case http.MethodDelete:
		s.handleMutateTarget(w, r, "target.clear")
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (s *Server) handleIssueTargetApproval(w http.ResponseWriter, r *http.Request) {
	caller, ok := s.requireTargetOperator(w, r)
	if !ok {
		return
	}
	var req TargetApprovalIssueRequest
	if !requireJSONContentType(w, r) {
		return
	}
	if decodeStrictJSONRequest(r, maxTargetRequestBytes, &req) != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid target approval request")
		return
	}
	kind, err := targetOperationKind(req.Kind)
	if err != nil || domain.ValidateReason(req.Reason) != nil || domain.ValidateIdempotencyKey(req.IdempotencyKey) != nil ||
		req.ValidForMillis < 1000 || req.ValidForMillis > int64((5*time.Minute)/time.Millisecond) ||
		(kind == "target.clear" && (req.Reference != "" || len(req.Aliases) != 0)) {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid target approval request")
		return
	}
	grant, err := s.targetCoordinator.IssueApproval(r.Context(), app.TargetApprovalIssueParams{
		Kind: kind, Reference: req.Reference, Aliases: req.Aliases, Caller: caller,
		Reason: req.Reason, IdempotencyKey: req.IdempotencyKey,
		ValidFor: time.Duration(req.ValidForMillis) * time.Millisecond,
	})
	if err != nil {
		s.writeTargetMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, TargetApprovalIssueResponse{
		SchemaVersion: SchemaVersion, ApprovalID: grant.ApprovalID,
		Deadline: grant.Deadline.UTC().Format(time.RFC3339Nano), ExpiresAt: grant.ExpiresAt.UTC().Format(time.RFC3339Nano),
		Operation: TargetOperationDTO{
			Kind: string(grant.Operation.Kind), Target: string(grant.Operation.Target), Reason: grant.Operation.Reason,
			IdempotencyKey: grant.Operation.IdempotencyKey, Parameters: grant.Operation.Parameters,
		},
		Receipt: receipt.ConvertToDTO(grant.Receipt),
	})
}

func (s *Server) handleGetTarget(w http.ResponseWriter, r *http.Request) {
	caller, ok := getCallerContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthenticated caller")
		return
	}
	if !caller.HasScope(domain.ScopeMachineRead) {
		writeError(w, http.StatusForbidden, "forbidden", "target observation requires machine:read authority")
		return
	}
	resolution, err := s.targetCoordinator.Show(r.Context())
	if err != nil {
		writeTargetResolutionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, TargetResponse{SchemaVersion: SchemaVersion, Target: targetDTO(resolution)})
}

func (s *Server) handleMutateTarget(w http.ResponseWriter, r *http.Request, kind domain.OperationKind) {
	caller, ok := s.requireTargetOperator(w, r)
	if !ok {
		return
	}
	var req TargetMutationRequest
	if !requireJSONContentType(w, r) {
		return
	}
	if decodeStrictJSONRequest(r, maxTargetRequestBytes, &req) != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid target mutation request")
		return
	}
	deadline, err := ResolveSessionDeadline(req.ApprovalID, req.Deadline)
	if err != nil || domain.ValidateReason(req.Reason) != nil || domain.ValidateIdempotencyKey(req.IdempotencyKey) != nil ||
		domain.ValidateApprovalID(req.ApprovalID) != nil || (kind == "target.clear" && (req.Reference != "" || len(req.Aliases) != 0)) {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid target mutation request")
		return
	}
	result, err := s.targetCoordinator.Mutate(r.Context(), app.TargetMutationParams{
		Kind: kind, Reference: req.Reference, Aliases: req.Aliases, Caller: caller, Reason: req.Reason,
		IdempotencyKey: req.IdempotencyKey, Deadline: deadline, ApprovalID: req.ApprovalID,
	})
	if err != nil {
		s.writeTargetMutationError(w, err)
		return
	}
	receiptDTO := receipt.ConvertToDTO(result.Receipt)
	writeJSON(w, http.StatusOK, TargetResponse{
		SchemaVersion: SchemaVersion, Target: targetDTO(result.Resolution), Receipt: &receiptDTO,
	})
}

func (s *Server) requireTargetOperator(w http.ResponseWriter, r *http.Request) (domain.ActorContext, bool) {
	caller, ok := getCallerContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthenticated caller")
		return domain.ActorContext{}, false
	}
	if caller.IsDelegated() || !caller.HasScope(domain.ScopeTargetAdmin) {
		writeError(w, http.StatusForbidden, "forbidden", "target mutation requires operator target:admin authority")
		return domain.ActorContext{}, false
	}
	return caller, true
}

func (s *Server) writeTargetMutationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, target.ErrAccessDenied), errors.Is(err, target.ErrApprovalRequired), errors.Is(err, domain.ErrApprovalConsumed),
		errors.Is(err, domain.ErrApprovalExpired), errors.Is(err, domain.ErrApprovalActorMismatch), errors.Is(err, domain.ErrApprovalTargetMismatch),
		errors.Is(err, domain.ErrApprovalFingerprintMismatch), errors.Is(err, domain.ErrApprovalKeyMismatch), errors.Is(err, domain.ErrApprovalClassMismatch):
		writeError(w, http.StatusForbidden, "forbidden", "target approval is missing, expired, consumed, or does not match")
	case errors.Is(err, receipt.ErrIdempotencyCollision), errors.Is(err, target.ErrMutationCollision):
		writeError(w, http.StatusConflict, "conflict", "target idempotency key conflicts with existing authority")
	case errors.Is(err, target.ErrMutationDrift), errors.Is(err, target.ErrMutationFinalization), errors.Is(err, target.ErrInsecureState),
		errors.Is(err, target.ErrCommittedNotDurable), errors.Is(err, target.ErrDurabilityPending):
		writeError(w, http.StatusServiceUnavailable, "target_unavailable", "target authority requires fail-closed reconciliation")
	case errors.Is(err, target.ErrNoDefault), errors.Is(err, target.ErrDifferentTarget), errors.Is(err, target.ErrInventoryRefresh),
		errors.Is(err, domain.ErrMachineReferenceMiss), errors.Is(err, domain.ErrMachineReferenceAmbig), errors.Is(err, domain.ErrMachineReferenceStale):
		writeTargetResolutionError(w, err)
	default:
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid target authority request")
	}
}

func targetOperationKind(value string) (domain.OperationKind, error) {
	switch strings.TrimSpace(value) {
	case "enroll", "target.enroll":
		return "target.enroll", nil
	case "clear", "target.clear":
		return "target.clear", nil
	default:
		return "", domain.ErrInvalidOperationKind
	}
}

func targetDTO(resolution app.TargetResolution) TargetDTO {
	return TargetDTO{Locator: resolution.Locator.String(), ProviderVMID: resolution.ProviderVMID}
}

func requireJSONContentType(w http.ResponseWriter, r *http.Request) bool {
	contentType := strings.ToLower(r.Header.Get("Content-Type"))
	if contentType != "" && !strings.HasPrefix(contentType, "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "invalid_argument", "Content-Type must be application/json")
		return false
	}
	return true
}
