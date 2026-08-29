package daemon

import (
	"time"

	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

// SchemaVersion is the version identifier for daemon API payloads.
const SchemaVersion = "1"

// ErrorEnvelope represents a sanitized, machine-readable JSON error response.
type ErrorEnvelope struct {
	SchemaVersion string     `json:"schema_version"`
	Error         ErrorField `json:"error"`
}

// ErrorField is the structured error description.
type ErrorField struct {
	Category string `json:"category"`
	Message  string `json:"message"`
}

// HealthResponse represents the response to GET /v1/health.
type HealthResponse struct {
	SchemaVersion string    `json:"schema_version"`
	Status        string    `json:"status"`
	Version       string    `json:"version"`
	StartedAt     time.Time `json:"started_at"`
	PID           int       `json:"pid"`
}

// CreateOperationRequest is the payload for POST /v1/operations.
type CreateOperationRequest struct {
	Kind           string         `json:"kind"`
	Target         string         `json:"target"`
	Reason         string         `json:"reason"`
	IdempotencyKey string         `json:"idempotency_key"`
	TimeoutSeconds int            `json:"timeout_seconds,omitempty"`
	Deadline       *time.Time     `json:"deadline,omitempty"`
	Parameters     map[string]any `json:"parameters,omitempty"`
}

// OperationDTO is the JSON representation of an operation.
type OperationDTO struct {
	SchemaVersion  string         `json:"schema_version"`
	OperationID    string         `json:"operation_id"`
	Kind           string         `json:"kind"`
	Target         string         `json:"target"`
	Actor          string         `json:"actor"`
	State          string         `json:"state"`
	RequestedClass string         `json:"requested_class,omitempty"`
	EffectiveClass string         `json:"effective_class,omitempty"`
	Fingerprint    string         `json:"fingerprint,omitempty"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	CreatedAt      string         `json:"created_at"`
	AdmittedAt     string         `json:"admitted_at,omitempty"`
	RunningAt      string         `json:"running_at,omitempty"`
	CompletedAt    string         `json:"completed_at,omitempty"`
	Deadline       string         `json:"deadline,omitempty"`
	ReceiptID      string         `json:"receipt_id,omitempty"`
	ErrorCategory  string         `json:"error_category,omitempty"`
	ErrorMessage   string         `json:"error_message,omitempty"`
	Parameters     map[string]any `json:"parameters,omitempty"`
}

// ConvertToOperationDTO converts a domain.OperationRecord to an OperationDTO.
func ConvertToOperationDTO(rec domain.OperationRecord) OperationDTO {
	dto := OperationDTO{
		SchemaVersion:  SchemaVersion,
		OperationID:    rec.ID,
		Kind:           string(rec.Kind),
		Target:         string(rec.Target),
		Actor:          string(rec.Actor),
		State:          string(rec.State),
		RequestedClass: string(rec.RequestedClass),
		EffectiveClass: string(rec.EffectiveClass),
		Fingerprint:    string(rec.Fingerprint),
		IdempotencyKey: rec.IdempotencyKey,
		CreatedAt:      rec.CreatedAt.UTC().Format(time.RFC3339Nano),
		ReceiptID:      string(rec.ReceiptID),
		ErrorCategory:  rec.ErrorCategory,
		ErrorMessage:   rec.ErrorMessage,
		Parameters:     rec.Parameters,
	}
	if !rec.AdmittedAt.IsZero() {
		dto.AdmittedAt = rec.AdmittedAt.UTC().Format(time.RFC3339Nano)
	}
	if !rec.RunningAt.IsZero() {
		dto.RunningAt = rec.RunningAt.UTC().Format(time.RFC3339Nano)
	}
	if !rec.CompletedAt.IsZero() {
		dto.CompletedAt = rec.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	if !rec.Deadline.IsZero() {
		dto.Deadline = rec.Deadline.UTC().Format(time.RFC3339Nano)
	}
	return dto
}

// OperationListResponse is the response to GET /v1/operations.
type OperationListResponse struct {
	SchemaVersion string         `json:"schema_version"`
	Operations    []OperationDTO `json:"operations"`
}

// CancelOperationRequest is the payload for POST /v1/operations/{id}/cancel.
type CancelOperationRequest struct {
	Reason string `json:"reason"`
}

// CancelOperationResponse is the response to POST /v1/operations/{id}/cancel.
type CancelOperationResponse struct {
	SchemaVersion string `json:"schema_version"`
	Status        string `json:"status"`
	OperationID   string `json:"operation_id"`
}

// AuditListResponse is the response to GET /v1/audit.
type AuditListResponse struct {
	SchemaVersion string        `json:"schema_version"`
	Events        []audit.Event `json:"events"`
}

// ReceiptResponse is the response to GET /v1/receipts/{id}.
type ReceiptResponse struct {
	SchemaVersion string      `json:"schema_version"`
	Receipt       receipt.DTO `json:"receipt"`
}

// StopDaemonResponse is the response to POST /v1/daemon/stop.
type StopDaemonResponse struct {
	SchemaVersion string `json:"schema_version"`
	Status        string `json:"status"`
}
