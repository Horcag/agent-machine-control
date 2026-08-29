package daemon

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/Horcag/agent-machine-control/internal/buildinfo"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	_, ok := getCallerContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
		return
	}

	writeJSON(w, http.StatusOK, HealthResponse{
		SchemaVersion: SchemaVersion,
		Status:        "ok",
		Version:       buildinfo.Version(),
		StartedAt:     s.startedAt,
		PID:           s.pid,
	})
}

func (s *Server) handleGetAudit(w http.ResponseWriter, r *http.Request) {
	caller, ok := getCallerContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
		return
	}

	if !caller.CallerPermissions.Has("audit:read") {
		writeError(w, http.StatusForbidden, "forbidden", "unrestricted audit query requires operator authority")
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	eventsList, err := s.auditStore.Tail(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to read audit events")
		return
	}

	writeJSON(w, http.StatusOK, AuditListResponse{
		SchemaVersion: SchemaVersion,
		Events:        eventsList,
	})
}

func (s *Server) handleGetReceipt(w http.ResponseWriter, r *http.Request, receiptID string) {
	caller, ok := getCallerContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
		return
	}

	if err := domain.ValidateReceiptID(receiptID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_argument", "invalid receipt ID format")
		return
	}

	rcpt, err := s.receiptStore.Get(receiptID)
	if err != nil {
		if errors.Is(err, receipt.ErrReceiptNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "receipt not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to fetch receipt")
		return
	}

	isOp := caller.CallerPermissions.Has("audit:read") || caller.CallerPermissions.Has("operation:cancel")
	if !isOp && rcpt.Actor != caller.EffectiveActor {
		writeError(w, http.StatusNotFound, "not_found", "receipt not found")
		return
	}

	writeJSON(w, http.StatusOK, ReceiptResponse{
		SchemaVersion: SchemaVersion,
		Receipt:       receipt.ConvertToDTO(*rcpt),
	})
}

func (s *Server) handleStopDaemon(w http.ResponseWriter, r *http.Request) {
	caller, ok := getCallerContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
		return
	}

	isOp := caller.CallerPermissions.Has("operation:cancel") || caller.CallerPermissions.Has("audit:read")
	if !isOp {
		writeError(w, http.StatusForbidden, "forbidden", "daemon shutdown requires operator authority")
		return
	}

	writeJSON(w, http.StatusOK, StopDaemonResponse{
		SchemaVersion: SchemaVersion,
		Status:        "stopping",
	})

	go s.TriggerShutdown()
}

func (s *Server) handleListReceipts(w http.ResponseWriter, r *http.Request) {
	caller, ok := getCallerContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	isOp := caller.CallerPermissions.Has("audit:read") || caller.CallerPermissions.Has("operation:cancel")
	actorFilter := string(caller.EffectiveActor)
	if isOp {
		actorFilter = ""
	}

	receiptsList, err := s.receiptStore.List(limit, actorFilter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "failed to list receipts")
		return
	}

	dtos := make([]receipt.DTO, len(receiptsList))
	for i, r := range receiptsList {
		dtos[i] = receipt.ConvertToDTO(r)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"schema_version": SchemaVersion,
		"receipts":       dtos,
	})
}
