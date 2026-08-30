package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func (s *Server) handleIssueSessionApproval(w http.ResponseWriter, r *http.Request) {
	caller, ok := getCallerContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthenticated caller")
		return
	}
	if caller.IsDelegated() || !caller.HasScope(domain.ScopeSessionAdmin) {
		writeError(w, http.StatusForbidden, "forbidden", "session approval issuance requires operator session:admin authority")
		return
	}

	var req SessionApprovalIssueRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, 128*1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "malformed request body")
		return
	}
	if err := validateSessionApprovalIssueRequest(req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid session approval request")
		return
	}

	grant, rcpt, err := s.sessionService.IssueSessionMutationApproval(r.Context(), app.SessionApprovalIssueParams{
		Kind: domain.OperationKind(req.Kind), Caller: caller, Target: req.Target,
		SessionID: domain.SessionID(req.SessionID), Data: req.Data, Key: domain.ControlKey(req.Key),
		Reason: req.Reason, IdempotencyKey: req.IdempotencyKey,
		ValidFor: time.Duration(req.ValidForMillis) * time.Millisecond,
		Cols:     req.Cols, Rows: req.Rows, Term: req.Term, Force: req.Force,
	})
	if err != nil {
		s.mapSessionError(w, err)
		return
	}

	var rcptDTO *receipt.DTO
	if rcpt != nil {
		dto := receipt.ConvertToDTO(*rcpt)
		rcptDTO = &dto
	}
	writeJSON(w, http.StatusOK, SessionApprovalIssueResponse{
		SchemaVersion: SchemaVersion,
		ApprovalID:    grant.ApprovalID,
		Deadline:      grant.Deadline.UTC().Format(time.RFC3339Nano),
		ExpiresAt:     grant.ExpiresAt.UTC().Format(time.RFC3339Nano),
		Operation: SessionApprovalOperationDTO{
			Kind: string(grant.Operation.Kind), Target: string(grant.Operation.Target),
			Reason: grant.Operation.Reason, IdempotencyKey: grant.Operation.IdempotencyKey,
			Parameters: grant.Operation.Parameters,
		},
		Receipt: rcptDTO,
	})
}

func validateSessionApprovalIssueRequest(req SessionApprovalIssueRequest) error {
	if err := domain.ValidateReason(req.Reason); err != nil {
		return err
	}
	if err := domain.ValidateIdempotencyKey(req.IdempotencyKey); err != nil {
		return err
	}
	if req.ValidForMillis < int64(time.Second/time.Millisecond) || req.ValidForMillis > int64((5*time.Minute)/time.Millisecond) {
		return ErrInvalidSessionDeadline
	}
	switch req.Kind {
	case "session.open":
		return validateSessionOpenApprovalRequest(req)
	case "session.write":
		return validateSessionWriteApprovalRequest(req)
	case "session.control":
		return validateSessionControlApprovalRequest(req)
	case "session.close":
		return validateSessionCloseApprovalRequest(req)
	default:
		return domain.ErrInvalidOperationKind
	}
}

func validateSessionOpenApprovalRequest(req SessionApprovalIssueRequest) error {
	if req.SessionID != "" || req.Data != "" || req.Key != "" || req.Force {
		return domain.ErrNonCanonicalParameter
	}
	return domain.ValidateMachineGUID(req.Target)
}

func validateSessionWriteApprovalRequest(req SessionApprovalIssueRequest) error {
	if req.Data == "" || hasNonSessionApprovalFields(req) || req.Key != "" || req.Force {
		return domain.ErrNonCanonicalParameter
	}
	return domain.ValidateSessionID(req.SessionID)
}

func validateSessionControlApprovalRequest(req SessionApprovalIssueRequest) error {
	if hasNonSessionApprovalFields(req) || req.Data != "" || req.Force {
		return domain.ErrNonCanonicalParameter
	}
	if err := domain.ValidateSessionID(req.SessionID); err != nil {
		return err
	}
	return domain.ValidateControlKey(req.Key)
}

func validateSessionCloseApprovalRequest(req SessionApprovalIssueRequest) error {
	if hasNonSessionApprovalFields(req) || req.Data != "" || req.Key != "" {
		return domain.ErrNonCanonicalParameter
	}
	return domain.ValidateSessionID(req.SessionID)
}

func hasNonSessionApprovalFields(req SessionApprovalIssueRequest) bool {
	return req.Target != "" || req.Cols != 0 || req.Rows != 0 || req.Term != ""
}
