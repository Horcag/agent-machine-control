package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func (s *Server) dispatchSessions(w http.ResponseWriter, r *http.Request, path string) {
	if path == "sessions" {
		switch r.Method {
		case http.MethodPost:
			s.handleCreateSession(w, r)
		case http.MethodGet:
			s.handleListSessions(w, r)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
		return
	}

	if escapedPathContainsSeparator(r.URL.EscapedPath()) {
		s.mapSessionError(w, domain.ErrInvalidSessionID)
		return
	}
	sessID, subparts, err := parseSessionSubroute(path)
	if err != nil {
		s.mapSessionError(w, err)
		return
	}
	s.dispatchSessionSubroute(w, r, sessID, subparts)
}

func parseSessionSubroute(path string) (domain.SessionID, []string, error) {
	rest := strings.TrimPrefix(path, "sessions/")
	parts := strings.Split(rest, "/")
	if err := domain.ValidateSessionID(parts[0]); err != nil {
		return "", nil, err
	}
	return domain.SessionID(parts[0]), parts[1:], nil
}

func escapedPathContainsSeparator(escapedPath string) bool {
	escapedPath = strings.ToLower(escapedPath)
	return strings.Contains(escapedPath, "%2f") || strings.Contains(escapedPath, "%5c")
}

func (s *Server) dispatchSessionAction(w http.ResponseWriter, r *http.Request, sessID domain.SessionID, action string) {
	switch action {
	case "read":
		if r.Method == http.MethodGet {
			s.handleReadSession(w, r, sessID)
			return
		}
	case "write":
		if r.Method == http.MethodPost {
			s.handleWriteSession(w, r, sessID)
			return
		}
	case "control":
		if r.Method == http.MethodPost {
			s.handleControlSession(w, r, sessID)
			return
		}
	case "wait":
		if r.Method == http.MethodPost {
			s.handleWaitSession(w, r, sessID)
			return
		}
	case "close":
		if r.Method == http.MethodPost {
			s.handleCloseSession(w, r, sessID)
			return
		}
	}
	writeError(w, http.StatusNotFound, "not_found", "endpoint not found")
}

func (s *Server) dispatchSessionSubroute(w http.ResponseWriter, r *http.Request, sessID domain.SessionID, subparts []string) {
	if len(subparts) == 0 {
		if r.Method == http.MethodGet {
			s.handleGetSession(w, r, sessID)
			return
		}
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}

	if len(subparts) != 1 {
		writeError(w, http.StatusNotFound, "not_found", "endpoint not found")
		return
	}

	s.dispatchSessionAction(w, r, sessID, subparts[0])
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	caller, ok := getCallerContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthenticated caller")
		return
	}

	var req SessionOpenRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, 64*1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "malformed request body")
		return
	}

	if err := domain.ValidateMachineGUID(req.Target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid target machine GUID")
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

	obs, rcpt, err := s.sessionService.OpenSession(r.Context(), app.SessionOpenParams{
		Target:         req.Target,
		Caller:         caller,
		Reason:         req.Reason,
		IdempotencyKey: req.IdempotencyKey,
		Timeout:        timeout,
		Deadline:       deadline,
		Cols:           req.Cols,
		Rows:           req.Rows,
		Term:           req.Term,
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

	writeJSON(w, http.StatusOK, SessionOpenResponse{
		SchemaVersion: SchemaVersion,
		Session:       ConvertToSessionDTO(*obs),
		Receipt:       rcptDTO,
	})
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	caller, ok := getCallerContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthenticated caller")
		return
	}

	target := domain.MachineRef(r.URL.Query().Get("machine"))
	if target != "" {
		if err := domain.ValidateMachineGUID(string(target)); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_argument", "invalid machine GUID")
			return
		}
	}

	list, err := s.sessionService.ListSessions(r.Context(), caller, target)
	if err != nil {
		s.mapSessionError(w, err)
		return
	}

	dtos := make([]SessionDTO, len(list))
	for i, obs := range list {
		dtos[i] = ConvertToSessionDTO(obs)
	}

	writeJSON(w, http.StatusOK, SessionListResponse{
		SchemaVersion: SchemaVersion,
		Sessions:      dtos,
	})
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request, id domain.SessionID) {
	caller, ok := getCallerContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthenticated caller")
		return
	}

	obs, err := s.sessionService.GetSession(r.Context(), id, caller)
	if err != nil {
		s.mapSessionError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, SessionOpenResponse{
		SchemaVersion: SchemaVersion,
		Session:       ConvertToSessionDTO(*obs),
	})
}
