package daemon

import (
	"net/http"

	"github.com/Horcag/agent-machine-control/internal/app"
)

func (s *Server) dispatchDesktop(w http.ResponseWriter, r *http.Request, path string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	actor, ok := getCallerContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthenticated caller")
		return
	}
	if s.consoleService == nil {
		writeError(w, http.StatusNotImplemented, "capability_unavailable", "VM console unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128*1024)
	switch path {
	case "desktop/action":
		var req app.DesktopActionRequest
		if decodeStrictJSONObject(r.Body, &req) != nil {
			writeError(w, http.StatusBadRequest, "invalid_argument", "invalid desktop action")
			return
		}
		if s.desktopService == nil {
			writeError(w, http.StatusNotImplemented, "capability_unavailable", "guest desktop unavailable")
			return
		}
		out, err := s.desktopService.Action(r.Context(), actor, req)
		if err != nil {
			writeConsoleError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	default:
		s.dispatchDesktopLab(w, r, path)
	}
}

func (s *Server) dispatchDesktopLab(w http.ResponseWriter, r *http.Request, path string) {
	actor, _ := getCallerContext(r.Context())
	switch path {
	case "desktop/lab/issue":
		var req app.ConsoleLabGrantIssueRequest
		if decodeStrictJSONObject(r.Body, &req) != nil {
			writeError(w, http.StatusBadRequest, "invalid_argument", "invalid lab grant")
			return
		}
		grant, rcpt, err := s.consoleService.IssueLabGrant(r.Context(), actor, req)
		if err != nil {
			writeConsoleError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Grant   app.ConsoleLabGrant `json:"grant"`
			Receipt any                 `json:"receipt"`
		}{grant, rcpt})
	case "desktop/lab/status":
		var req struct {
			GrantID string `json:"grant_id"`
		}
		if decodeStrictJSONObject(r.Body, &req) != nil {
			writeError(w, http.StatusBadRequest, "invalid_argument", "invalid lab grant")
			return
		}
		out, err := s.consoleService.LabGrantStatus(r.Context(), actor, req.GrantID)
		if err != nil {
			writeConsoleError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	case "desktop/lab/active":
		var req struct {
			Target string `json:"target,omitempty"`
		}
		if decodeStrictJSONObject(r.Body, &req) != nil {
			writeError(w, http.StatusBadRequest, "invalid_argument", "invalid lab target")
			return
		}
		out, err := s.consoleService.ActiveLabGrant(r.Context(), actor, req.Target)
		if err != nil {
			writeConsoleError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	case "desktop/lab/revoke":
		var req app.ConsoleLabGrantRevokeRequest
		if decodeStrictJSONObject(r.Body, &req) != nil {
			writeError(w, http.StatusBadRequest, "invalid_argument", "invalid lab grant")
			return
		}
		out, err := s.consoleService.RevokeLabGrant(r.Context(), actor, req)
		if err != nil {
			writeConsoleError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	default:
		writeError(w, http.StatusNotFound, "not_found", "endpoint not found")
	}
}
