package daemon

import (
	"errors"
	"net/http"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
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
	case "console/record/status":
		s.dispatchRecordingStatus(w, r, caller)
	case "console/record":
		s.dispatchRecording(w, r, caller)
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
		result, err := s.consoleService.InputResult(r.Context(), caller, req)
		if err != nil {
			writeActionError(w, err, result.Receipt, result.CachedReceipt, "console.input")
			return
		}
		if result.CachedReceipt {
			w.Header().Set("X-AMC-Cached-Receipt", "true")
		}
		writeJSON(w, http.StatusOK, result.Receipt)
	default:
		writeError(w, http.StatusNotFound, "not_found", "endpoint not found")
	}
}

func writeConsoleError(w http.ResponseWriter, err error) {
	status, field := consoleError(err)
	writeError(w, status, field.Category, field.Message)
}

func consoleError(err error) (int, ErrorField) {
	if errors.Is(err, domain.ErrClipboardUncertain) {
		message, _ := domain.CanonicalFailureMessage(domain.FailureCategoryClipboardUncertain)
		return http.StatusConflict, ErrorField{Category: domain.FailureCategoryClipboardUncertain, Message: message}
	}
	if denied, ok := errors.AsType[*app.PolicyDeniedError](err); ok {
		return http.StatusForbidden, ErrorField{Category: string(denied.Reason), Message: denied.Message}
	}
	if class, known := classifyTargetFailure(err); known {
		return class.status, ErrorField{Category: class.category, Message: class.message}
	}
	// Provider messages can contain guest input or framebuffer details.
	return http.StatusBadRequest, ErrorField{Category: "console_failed", Message: "console request failed"}
}

func (s *Server) dispatchRecordingStatus(w http.ResponseWriter, r *http.Request, caller domain.ActorContext) {
	var req app.ConsoleRecordStatusRequest
	if decodeStrictJSONObject(r.Body, &req) != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid recording status request")
		return
	}
	out, err := s.consoleService.RecordStatus(r.Context(), caller, req)
	if errors.Is(err, app.ErrRecordingStatusInconclusive) {
		writeError(w, http.StatusConflict, "recording_status_inconclusive", "recording status is inconclusive")
		return
	}
	if err != nil {
		writeConsoleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) dispatchRecording(w http.ResponseWriter, r *http.Request, caller domain.ActorContext) {
	var req app.ConsoleRecordRequest
	if decodeStrictJSONObject(r.Body, &req) != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid recording request")
		return
	}
	out, err := s.consoleService.Record(r.Context(), caller, req)
	if err != nil {
		writeConsoleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
