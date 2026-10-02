package daemon

import (
	"errors"
	"net/http"

	"github.com/Horcag/agent-machine-control/internal/app"
)

func (s *Server) dispatchConsole(w http.ResponseWriter, r *http.Request, path string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	caller, ok := getCallerContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthenticated caller")
		return
	}
	if s.consoleService == nil {
		writeError(w, http.StatusNotImplemented, "capability_unavailable", "console provider unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	switch path {
	case "console/screenshot":
		var req app.ConsoleScreenshotRequest
		if err := decodeStrictJSONObject(r.Body, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_argument", "invalid console screenshot request")
			return
		}
		frame, err := s.consoleService.Screenshot(r.Context(), caller, req)
		if err != nil {
			writeConsoleError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, frame)
	case "console/input":
		var req app.ConsoleInputRequest
		if err := decodeStrictJSONObject(r.Body, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_argument", "invalid console input request")
			return
		}
		result, err := s.consoleService.Input(r.Context(), caller, req)
		if err != nil {
			writeConsoleError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	default:
		writeError(w, http.StatusNotFound, "not_found", "endpoint not found")
	}
}

func writeConsoleError(w http.ResponseWriter, err error) {
	if denied, ok := errors.AsType[*app.PolicyDeniedError](err); ok {
		writeError(w, http.StatusForbidden, string(denied.Reason), denied.Message)
		return
	}
	if isTargetResolutionFailure(err) {
		writeTargetResolutionError(w, err)
		return
	}
	// Provider messages can contain guest input or framebuffer details.
	writeError(w, http.StatusBadRequest, "console_failed", "console request failed")
}
