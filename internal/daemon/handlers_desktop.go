package daemon

import (
	"bytes"
	"errors"
	"net/http"
	"slices"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

// DesktopErrorEnvelope carries terminal evidence only, never a partial guest response.
type DesktopErrorEnvelope struct {
	ErrorEnvelope
	Receipt        *domain.Receipt `json:"receipt,omitempty"`
	CachedReceipt  bool            `json:"cached_receipt"`
	ReceiptInvalid bool            `json:"receipt_invalid,omitempty"`
}

func (e *DesktopErrorEnvelope) UnmarshalJSON(data []byte) error {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return err
	}
	type wire DesktopErrorEnvelope
	var decoded wire
	if err := decodeStrictJSONObject(bytes.NewReader(data), &decoded); err != nil {
		return err
	}
	*e = DesktopErrorEnvelope(decoded)
	return nil
}

// ValidateReceipt rejects unsafe or contradictory evidence at both transport ends.
func (e DesktopErrorEnvelope) ValidateReceipt(status int) error {
	return e.ValidateReceiptForOperation(status, "desktop.action")
}

func (e DesktopErrorEnvelope) ValidateReceiptForOperation(status int, kind domain.OperationKind) error {
	invalid := errors.New("invalid desktop failure receipt")
	if e.ReceiptInvalid || e.SchemaVersion != SchemaVersion {
		return invalid
	}
	if e.Receipt == nil {
		if e.CachedReceipt {
			return invalid
		}
		return nil
	}
	r := e.Receipt
	if r.Validate() != nil || r.OperationKind != kind || r.Class != domain.ClassDestructivePrivileged || r.RedactionStatus != domain.RedactionApplied {
		return invalid
	}
	if r.Outcome.Status == domain.OutcomeAborted && !slices.Contains(r.EvidenceRefs, domain.DesktopDispatchEvidence) {
		return invalid
	}
	if !matchesDesktopFailure(status, e.Error.Category, r.Outcome) {
		return invalid
	}
	return nil
}

func matchesDesktopFailure(status int, category string, outcome domain.ExecutionOutcome) bool {
	type failure struct {
		status            int
		outcome           domain.OutcomeStatus
		category, message string
	}
	clipboardMessage, _ := domain.CanonicalFailureMessage(domain.FailureCategoryClipboardUncertain)
	known := map[string]failure{
		"console_failed":                         {http.StatusBadRequest, domain.OutcomeFailed, "", ""},
		domain.FailureCategoryClipboardUncertain: {http.StatusConflict, domain.OutcomeFailed, domain.FailureCategoryClipboardUncertain, clipboardMessage},
		"timeout":                                {http.StatusGatewayTimeout, domain.OutcomeAborted, domain.FailureCategoryDeadlineExceeded, "operation deadline exceeded"},
		"cancelled":                              {http.StatusRequestTimeout, domain.OutcomeAborted, domain.FailureCategoryCallerCanceled, "operation cancelled"},
	}
	want, ok := known[category]
	return ok && status == want.status && outcome.Status == want.outcome && outcome.ErrorCategory == want.category && outcome.ErrorMessage == want.message
}

func writeDesktopError(w http.ResponseWriter, err error, out app.DesktopActionResult) {
	writeActionError(w, err, out.Receipt, out.CachedReceipt, "desktop.action")
}

func writeActionError(w http.ResponseWriter, err error, rcpt *domain.Receipt, cached bool, kind domain.OperationKind) {
	status, field := consoleError(err)
	env := DesktopErrorEnvelope{ErrorEnvelope: ErrorEnvelope{SchemaVersion: SchemaVersion, Error: field}, Receipt: rcpt, CachedReceipt: cached}
	if env.ValidateReceiptForOperation(status, kind) != nil {
		env.Receipt, env.CachedReceipt = nil, false
		env.ReceiptInvalid = true
	}
	writeJSON(w, status, env)
}

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
			writeDesktopError(w, err, out)
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
