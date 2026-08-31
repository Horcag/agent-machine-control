package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/target"
)

func (s *Server) handleReadSession(w http.ResponseWriter, r *http.Request, id domain.SessionID) {
	caller, ok := getCallerContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthenticated caller")
		return
	}

	afterSeq := uint64(0)
	if seqStr := r.URL.Query().Get("after_seq"); seqStr != "" {
		val, err := strconv.ParseUint(seqStr, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_argument", "invalid after_seq")
			return
		}
		afterSeq = val
	}

	limitBytes := 64 * 1024
	if limStr := r.URL.Query().Get("limit_bytes"); limStr != "" {
		val, err := strconv.Atoi(limStr)
		if err != nil || val <= 0 || val > domain.MaxSessionWriteBytes {
			writeError(w, http.StatusBadRequest, "invalid_argument", "invalid limit_bytes")
			return
		}
		limitBytes = val
	}

	chunks, nextSeq, lossBytes, hasMore, obs, err := s.sessionService.ReadSession(r.Context(), id, caller, afterSeq, limitBytes)
	if err != nil {
		s.mapSessionError(w, err)
		return
	}

	isClosed := obs.State.IsTerminal()

	writeJSON(w, http.StatusOK, SessionReadResponse{
		SchemaVersion: SchemaVersion,
		SessionID:     string(id),
		Chunks:        ConvertToChunkDTOs(chunks),
		NextSeq:       nextSeq,
		LossBytes:     lossBytes,
		HasMore:       hasMore,
		Closed:        isClosed,
		ExitCode:      obs.ExitCode,
	})
}

func (s *Server) handleWriteSession(w http.ResponseWriter, r *http.Request, id domain.SessionID) {
	caller, ok := getCallerContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthenticated caller")
		return
	}

	var req SessionWriteRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, 64*1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "malformed request body")
		return
	}

	if err := domain.ValidateReason(req.Reason); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid reason")
		return
	}
	if err := domain.ValidateIdempotencyKey(req.IdempotencyKey); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid idempotency key")
		return
	}

	timeout, err := ResolveSessionTimeout(req.TimeoutSeconds, req.TimeoutMillis, 30*time.Second)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid session timeout")
		return
	}
	deadline, err := ResolveSessionDeadline(req.ApprovalID, req.Deadline)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid session deadline")
		return
	}

	n, rcpt, err := s.sessionService.WriteSession(r.Context(), app.SessionWriteParams{
		SessionID:      id,
		Caller:         caller,
		Data:           req.Data,
		Reason:         req.Reason,
		IdempotencyKey: req.IdempotencyKey,
		Timeout:        timeout,
		Deadline:       deadline,
		ApprovalID:     req.ApprovalID,
		Approval:       req.Approval,
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

	writeJSON(w, http.StatusOK, SessionWriteResponse{
		SchemaVersion: SchemaVersion,
		BytesWritten:  n,
		Receipt:       rcptDTO,
	})
}

func (s *Server) handleControlSession(w http.ResponseWriter, r *http.Request, id domain.SessionID) {
	caller, ok := getCallerContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthenticated caller")
		return
	}

	var req SessionControlRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, 64*1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "malformed request body")
		return
	}

	if err := domain.ValidateReason(req.Reason); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid reason")
		return
	}
	if err := domain.ValidateIdempotencyKey(req.IdempotencyKey); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid idempotency key")
		return
	}

	key, err := domain.NormalizeControlKey(req.Key)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid control key")
		return
	}

	timeout, err := ResolveSessionTimeout(req.TimeoutSeconds, req.TimeoutMillis, 30*time.Second)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid session timeout")
		return
	}
	deadline, err := ResolveSessionDeadline(req.ApprovalID, req.Deadline)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid session deadline")
		return
	}

	rcpt, err := s.sessionService.ControlSession(r.Context(), app.SessionControlParams{
		SessionID:      id,
		Caller:         caller,
		Key:            key,
		Reason:         req.Reason,
		IdempotencyKey: req.IdempotencyKey,
		Timeout:        timeout,
		Deadline:       deadline,
		ApprovalID:     req.ApprovalID,
		Approval:       req.Approval,
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

	writeJSON(w, http.StatusOK, SessionControlResponse{
		SchemaVersion: SchemaVersion,
		Status:        "sent",
		Receipt:       rcptDTO,
	})
}

func (s *Server) handleWaitSession(w http.ResponseWriter, r *http.Request, id domain.SessionID) {
	caller, ok := getCallerContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthenticated caller")
		return
	}

	var req SessionWaitRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, 64*1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "malformed request body")
		return
	}

	if req.SettleMs < 0 {
		writeError(w, http.StatusBadRequest, "invalid_argument", "settle_ms must not be negative")
		return
	}
	if len(req.Regex) > domain.MaxSessionRegexPatternLength {
		writeError(w, http.StatusBadRequest, "invalid_argument", "regex exceeds max length")
		return
	}
	settle := domain.DefaultSettleTime
	if req.SettleMs > 0 {
		settle = time.Duration(req.SettleMs) * time.Millisecond
	}

	timeout, err := ResolveSessionTimeout(req.TimeoutSeconds, req.TimeoutMillis, domain.DefaultWaitTimeout)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid session timeout")
		return
	}

	chunks, nextSeq, lossBytes, matched, obs, err := s.sessionService.WaitSession(r.Context(), id, caller, settle, req.Regex, req.AfterSeq, timeout)
	if err != nil {
		s.mapSessionError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, SessionWaitResponse{
		SchemaVersion: SchemaVersion,
		SessionID:     string(id),
		Chunks:        ConvertToChunkDTOs(chunks),
		NextSeq:       nextSeq,
		LossBytes:     lossBytes,
		Matched:       matched,
		Closed:        obs.State.IsTerminal(),
	})
}

func (s *Server) handleCloseSession(w http.ResponseWriter, r *http.Request, id domain.SessionID) {
	caller, ok := getCallerContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthenticated caller")
		return
	}

	if r.Body == nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "session close requires request body")
		return
	}

	var req SessionCloseRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, 64*1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "malformed request body")
		return
	}

	if err := domain.ValidateReason(req.Reason); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid reason")
		return
	}
	if err := domain.ValidateIdempotencyKey(req.IdempotencyKey); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid idempotency key")
		return
	}

	timeout, err := ResolveSessionTimeout(req.TimeoutSeconds, req.TimeoutMillis, 30*time.Second)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid session timeout")
		return
	}
	deadline, err := ResolveSessionDeadline(req.ApprovalID, req.Deadline)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid session deadline")
		return
	}

	obs, rcpt, err := s.sessionService.CloseSession(r.Context(), app.SessionCloseParams{
		SessionID:      id,
		Caller:         caller,
		Reason:         req.Reason,
		IdempotencyKey: req.IdempotencyKey,
		Timeout:        timeout,
		Deadline:       deadline,
		ApprovalID:     req.ApprovalID,
		Approval:       req.Approval,
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

	writeJSON(w, http.StatusOK, SessionCloseResponse{
		SchemaVersion: SchemaVersion,
		Session:       ConvertToSessionDTO(*obs),
		Receipt:       rcptDTO,
	})
}

func mapSessionClientError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, domain.ErrSessionNotFound) || errors.Is(err, domain.ErrInvalidSessionID):
		writeError(w, http.StatusNotFound, "not_found", "session not found")
	case errors.Is(err, domain.ErrSessionAccessDenied):
		writeError(w, http.StatusForbidden, "forbidden", "session access denied")
	case errors.Is(err, domain.ErrSessionClosed):
		writeError(w, http.StatusConflict, "conflict", "session is closed")
	case errors.Is(err, domain.ErrSessionConflict) || errors.Is(err, receipt.ErrIdempotencyCollision):
		writeError(w, http.StatusConflict, "conflict", "session idempotency conflict")
	default:
		return false
	}
	return true
}

func (s *Server) mapSessionError(w http.ResponseWriter, err error) {
	var deniedErr *app.PolicyDeniedError
	if errors.As(err, &deniedErr) {
		writeError(w, http.StatusForbidden, string(deniedErr.Reason), deniedErr.Message)
		return
	}
	if mapSessionClientError(w, err) {
		return
	}
	if mapSessionTargetError(w, err) {
		return
	}

	switch {
	case errors.Is(err, domain.ErrSessionWaitTimeout) || errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "timeout", "session wait timeout exceeded")
	case errors.Is(err, domain.ErrHostKeyMismatch):
		writeError(w, http.StatusBadGateway, "host_key_mismatch", "guest host key verification failed")
	case errors.Is(err, domain.ErrMissingHostKeyPin):
		writeError(w, http.StatusBadGateway, "missing_host_key_pin", "guest host key pin missing")
	case errors.Is(err, domain.ErrNonCanonicalParameter) || errors.Is(err, domain.ErrInvalidControlKey) || errors.Is(err, domain.ErrInvalidTerminalDimensions) || errors.Is(err, domain.ErrInvalidTerminalType) || errors.Is(err, domain.ErrInvalidApprovalRecord) || errors.Is(err, domain.ErrMissingDeadline):
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid parameter")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "internal server error")
	}
}

func mapSessionTargetError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, target.ErrNoDefault):
		writeTargetResolutionError(w, err)
	case errors.Is(err, target.ErrDifferentTarget), errors.Is(err, domain.ErrMachineReferenceMiss), errors.Is(err, domain.ErrMachineReferenceStale):
		writeError(w, http.StatusConflict, "target_mismatch", "target reference does not identify the enrolled target")
	case errors.Is(err, target.ErrInventoryRefresh), errors.Is(err, domain.ErrMachineHostUnavailable), errors.Is(err, domain.ErrMachineAccessDenied):
		writeTargetResolutionError(w, err)
	default:
		return false
	}
	return true
}

// MapSessionErrorForTest exposes error mapping logic for test assertion coverage.
func (s *Server) MapSessionErrorForTest(w http.ResponseWriter, err error) {
	s.mapSessionError(w, err)
}
