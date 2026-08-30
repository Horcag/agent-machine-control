package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/events"
	"github.com/Horcag/agent-machine-control/internal/operations"
	"github.com/Horcag/agent-machine-control/internal/target"
)

func (s *Server) handleCreateOperation(w http.ResponseWriter, r *http.Request) {
	caller, ok := getCallerContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
		return
	}

	ct := r.Header.Get("Content-Type")
	if ct != "" && !strings.HasPrefix(strings.ToLower(ct), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "invalid_argument", "Content-Type must be application/json")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var req CreateOperationRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid request body")
		return
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_argument", "trailing data in request body")
		return
	}

	op, timeout, err := buildOperationFromRequest(req, caller, s.now())
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid operation deadline")
		return
	}
	canonicalTarget, err := s.recoveryService.ResolveTargetReference(r.Context(), req.Target)
	if err != nil {
		writeTargetResolutionError(w, err)
		return
	}
	op.Target = canonicalTarget

	rec, wasExisting, err := s.opMgr.Submit(r.Context(), op, timeout)
	if err != nil {
		if errors.Is(err, operations.ErrOperationConflict) {
			writeError(w, http.StatusConflict, "conflict", "idempotency key conflict")
			return
		}
		if errors.Is(err, operations.ErrManagerShuttingDown) {
			writeError(w, http.StatusServiceUnavailable, "service_unavailable", "daemon is shutting down")
			return
		}
		if errors.Is(err, operations.ErrManagerBusy) {
			writeError(w, http.StatusTooManyRequests, "resource_exhausted", "daemon is busy")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_argument", err.Error())
		return
	}

	status := http.StatusAccepted
	if wasExisting || rec.State.IsTerminal() {
		status = http.StatusOK
	}

	dto := ConvertToOperationDTO(*rec)
	writeJSON(w, status, dto)
}

func writeTargetResolutionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, target.ErrNoDefault):
		writeError(w, http.StatusConflict, "target_not_enrolled", "no target is enrolled; enroll a local target first")
	case errors.Is(err, target.ErrDifferentTarget):
		writeError(w, http.StatusBadRequest, "target_mismatch", "target reference does not identify the enrolled target")
	case errors.Is(err, target.ErrInventoryRefresh), errors.Is(err, domain.ErrMachineHostUnavailable), errors.Is(err, domain.ErrMachineAccessDenied):
		writeError(w, http.StatusServiceUnavailable, "target_unavailable", "enrolled target inventory is unavailable")
	default:
		writeError(w, http.StatusBadRequest, "invalid_target", "target reference is invalid or stale")
	}
}

func (s *Server) handleListOperations(w http.ResponseWriter, r *http.Request) {
	caller, ok := getCallerContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
		return
	}

	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	opts := operations.ListOptions{
		State:   domain.OperationState(q.Get("state")),
		Machine: domain.MachineRef(q.Get("machine")),
		Limit:   limit,
	}

	records, err := s.opMgr.List(opts, caller)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list operations")
		return
	}

	dtos := make([]OperationDTO, len(records))
	for i, rec := range records {
		dtos[i] = ConvertToOperationDTO(rec)
	}

	writeJSON(w, http.StatusOK, OperationListResponse{
		SchemaVersion: SchemaVersion,
		Operations:    dtos,
	})
}

func (s *Server) handleGetOperation(w http.ResponseWriter, r *http.Request, opID string) {
	caller, ok := getCallerContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
		return
	}

	if err := domain.ValidateOperationID(opID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid operation ID format")
		return
	}

	rec, err := s.opMgr.Get(opID, caller)
	if err != nil {
		if errors.Is(err, operations.ErrOperationNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "operation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to get operation")
		return
	}

	writeJSON(w, http.StatusOK, ConvertToOperationDTO(*rec))
}

func (s *Server) handleCancelOperation(w http.ResponseWriter, r *http.Request, opID string) {
	caller, ok := getCallerContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
		return
	}

	if err := domain.ValidateOperationID(opID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid operation ID format")
		return
	}

	var req CancelOperationRequest
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid_argument", "invalid cancel request")
			return
		}
		var trailing any
		if err := dec.Decode(&trailing); err != io.EOF && err != nil {
			writeError(w, http.StatusBadRequest, "invalid_argument", "trailing data in request body")
			return
		}
	}

	if req.Reason == "" {
		req.Reason = "cancelled by client"
	}

	if err := s.opMgr.Cancel(opID, caller, req.Reason); err != nil {
		if errors.Is(err, operations.ErrOperationNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "operation not found")
			return
		}
		if errors.Is(err, operations.ErrOperationTerminal) {
			writeError(w, http.StatusConflict, "conflict", "operation is already in a terminal state")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to cancel operation")
		return
	}

	writeJSON(w, http.StatusOK, CancelOperationResponse{
		SchemaVersion: SchemaVersion,
		Status:        "cancelled",
		OperationID:   opID,
	})
}

func (s *Server) handleOperationEvents(w http.ResponseWriter, r *http.Request, opID string) {
	caller, ok := getCallerContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
		return
	}

	if err := domain.ValidateOperationID(opID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid operation ID format")
		return
	}

	// Verify operation access
	_, err := s.opMgr.Get(opID, caller)
	if err != nil {
		if errors.Is(err, operations.ErrOperationNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "operation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to access operation")
		return
	}

	afterSeq := extractAfterSeq(r)

	ch, unsub, err := s.eventHub.Subscribe(r.Context(), opID, afterSeq)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to subscribe to events")
		return
	}
	defer unsub()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal_error", "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	streamSSEEvents(r.Context(), w, flusher, ch)
}

func extractAfterSeq(r *http.Request) uint64 {
	if lastID := r.Header.Get("Last-Event-ID"); lastID != "" {
		if parsed, err := strconv.ParseUint(lastID, 10, 64); err == nil {
			return parsed
		}
	}
	if qSeq := r.URL.Query().Get("after_seq"); qSeq != "" {
		if parsed, err := strconv.ParseUint(qSeq, 10, 64); err == nil {
			return parsed
		}
	}
	return 0
}

func streamSSEEvents(ctx context.Context, w io.Writer, flusher http.Flusher, ch <-chan domain.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			data, err := events.FormatSSE(ev)
			if err != nil {
				return
			}
			if _, err := w.Write(data); err != nil {
				return
			}
			flusher.Flush()
			if ev.State.IsTerminal() || ev.EventType == "overflow" {
				return
			}
		}
	}
}

const (
	defaultOperationTimeout = 30 * time.Second
	maxOperationTimeout     = time.Hour
)

func resolveOperationDeadline(req CreateOperationRequest, now time.Time) (time.Time, time.Duration, error) {
	if req.TimeoutSeconds < 0 || req.TimeoutSeconds > int(maxOperationTimeout/time.Second) {
		return time.Time{}, 0, errors.New("operation timeout is outside the allowed range")
	}
	if req.TimeoutSeconds != 0 && req.Deadline != nil {
		return time.Time{}, 0, errors.New("operation timeout and deadline are mutually exclusive")
	}
	if req.Deadline != nil {
		deadline := req.Deadline.UTC()
		remaining := deadline.Sub(now)
		if remaining <= 0 || remaining > maxOperationTimeout {
			return time.Time{}, 0, errors.New("operation deadline is outside the allowed range")
		}
		return deadline, remaining, nil
	}

	timeout := defaultOperationTimeout
	if req.TimeoutSeconds != 0 {
		timeout = time.Duration(req.TimeoutSeconds) * time.Second
	}
	return now.Add(timeout), timeout, nil
}

func buildOperationFromRequest(req CreateOperationRequest, caller domain.ActorContext, now time.Time) (domain.Operation, time.Duration, error) {
	deadline, timeout, err := resolveOperationDeadline(req, now)
	if err != nil {
		return domain.Operation{}, 0, err
	}
	initialClass, capStr := resolveCapabilityAndClass(req.Kind, req.Parameters)

	op := domain.Operation{
		Kind:                domain.OperationKind(req.Kind),
		Target:              domain.MachineRef(req.Target),
		Actor:               caller,
		Reason:              req.Reason,
		Deadline:            deadline,
		IdempotencyKey:      req.IdempotencyKey,
		RequiredScopes:      []string{"machine:write"},
		RequiredCapability:  capStr,
		Classification:      initialClass,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters:          req.Parameters,
	}
	return op, timeout, nil
}

func resolveCapabilityAndClass(kind string, params map[string]any) (domain.OperationClass, string) {
	initialClass := domain.ClassReversibleMutation
	if kind == "machine.stop" && params != nil && params["mode"] == "turn-off" {
		initialClass = domain.ClassDestructivePrivileged
	} else if strings.HasPrefix(kind, "checkpoint.") {
		initialClass = domain.ClassDestructivePrivileged
	}

	var capStr string
	switch kind {
	case "machine.start":
		capStr = string(domain.CapabilityMachineStart)
	case "machine.stop":
		capStr = string(domain.CapabilityMachineStop)
	case "checkpoint.create":
		capStr = string(domain.CapabilityCheckpointCreate)
	case "checkpoint.restore":
		capStr = string(domain.CapabilityCheckpointRestore)
	}
	return initialClass, capStr
}
