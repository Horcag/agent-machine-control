package daemon

import (
	"bytes"
	"encoding/json"
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
	ApprovalID     string         `json:"approval_id,omitempty"`
	deadlineText   string
}

// MarshalJSON serializes approval deadlines in their canonical UTC form.
func (r CreateOperationRequest) MarshalJSON() ([]byte, error) {
	type requestWire struct {
		Kind           string         `json:"kind"`
		Target         string         `json:"target"`
		Reason         string         `json:"reason"`
		IdempotencyKey string         `json:"idempotency_key"`
		TimeoutSeconds int            `json:"timeout_seconds,omitempty"`
		Deadline       *string        `json:"deadline,omitempty"`
		Parameters     map[string]any `json:"parameters,omitempty"`
		ApprovalID     string         `json:"approval_id,omitempty"`
	}

	var deadline *string
	if r.Deadline != nil {
		text := r.Deadline.UTC().Format(time.RFC3339Nano)
		deadline = &text
	}
	return json.Marshal(requestWire{
		Kind: r.Kind, Target: r.Target, Reason: r.Reason, IdempotencyKey: r.IdempotencyKey,
		TimeoutSeconds: r.TimeoutSeconds, Deadline: deadline, Parameters: r.Parameters, ApprovalID: r.ApprovalID,
	})
}

// UnmarshalJSON retains the original deadline spelling for approval-bound validation.
func (r *CreateOperationRequest) UnmarshalJSON(data []byte) error {
	type requestWire struct {
		Kind           string          `json:"kind"`
		Target         string          `json:"target"`
		Reason         string          `json:"reason"`
		IdempotencyKey string          `json:"idempotency_key"`
		TimeoutSeconds int             `json:"timeout_seconds,omitempty"`
		Deadline       json.RawMessage `json:"deadline,omitempty"`
		Parameters     map[string]any  `json:"parameters,omitempty"`
		ApprovalID     string          `json:"approval_id,omitempty"`
	}
	var wire requestWire
	if err := decodeStrictJSONObject(bytes.NewReader(data), &wire); err != nil {
		return err
	}

	var deadline *time.Time
	var deadlineText string
	if len(wire.Deadline) != 0 && string(wire.Deadline) != "null" {
		if err := json.Unmarshal(wire.Deadline, &deadlineText); err != nil {
			return err
		}
		parsed, err := time.Parse(time.RFC3339Nano, deadlineText)
		if err != nil {
			return err
		}
		deadline = &parsed
	}
	*r = CreateOperationRequest{
		Kind: wire.Kind, Target: wire.Target, Reason: wire.Reason, IdempotencyKey: wire.IdempotencyKey,
		TimeoutSeconds: wire.TimeoutSeconds, Deadline: deadline, Parameters: wire.Parameters, ApprovalID: wire.ApprovalID,
		deadlineText: deadlineText,
	}
	return nil
}

// OperationApprovalIssueRequest is the operator-only exact operation approval payload.
type OperationApprovalIssueRequest struct {
	Kind           string         `json:"kind"`
	Target         string         `json:"target"`
	Reason         string         `json:"reason"`
	IdempotencyKey string         `json:"idempotency_key"`
	ValidForMillis int64          `json:"valid_for_ms"`
	Beneficiary    string         `json:"beneficiary,omitempty"`
	Parameters     map[string]any `json:"parameters,omitempty"`
}

// OperationApprovalIssueResponse returns only the reference and redacted operation identity.
type OperationApprovalIssueResponse struct {
	SchemaVersion string                        `json:"schema_version"`
	ApprovalID    string                        `json:"approval_id"`
	Deadline      string                        `json:"deadline"`
	ExpiresAt     string                        `json:"expires_at"`
	Operation     OperationApprovalOperationDTO `json:"operation"`
}

// OperationApprovalOperationDTO is the copy-safe canonical operation bound by a grant.
type OperationApprovalOperationDTO struct {
	Kind           string         `json:"kind"`
	Target         string         `json:"target"`
	Reason         string         `json:"reason"`
	IdempotencyKey string         `json:"idempotency_key"`
	Parameters     map[string]any `json:"parameters"`
}

// OperationDTO is the JSON representation of an operation.
type OperationDTO struct {
	SchemaVersion          string         `json:"schema_version"`
	OperationID            string         `json:"operation_id"`
	Kind                   string         `json:"kind"`
	Target                 string         `json:"target"`
	Actor                  string         `json:"actor"`
	State                  string         `json:"state"`
	RequestedClass         string         `json:"requested_class,omitempty"`
	EffectiveClass         string         `json:"effective_class,omitempty"`
	Fingerprint            string         `json:"fingerprint,omitempty"`
	IdempotencyFingerprint string         `json:"idempotency_fingerprint,omitempty"`
	IdempotencyKey         string         `json:"idempotency_key,omitempty"`
	CreatedAt              string         `json:"created_at"`
	AdmittedAt             string         `json:"admitted_at,omitempty"`
	RunningAt              string         `json:"running_at,omitempty"`
	CompletedAt            string         `json:"completed_at,omitempty"`
	Deadline               string         `json:"deadline,omitempty"`
	ReceiptID              string         `json:"receipt_id,omitempty"`
	ErrorCategory          string         `json:"error_category,omitempty"`
	ErrorMessage           string         `json:"error_message,omitempty"`
	Parameters             map[string]any `json:"parameters,omitempty"`
	ApprovalID             string         `json:"approval_id,omitempty"`
}

// ConvertToOperationDTO converts a domain.OperationRecord to an OperationDTO.
func ConvertToOperationDTO(rec domain.OperationRecord) OperationDTO {
	dto := OperationDTO{
		SchemaVersion:          SchemaVersion,
		OperationID:            rec.ID,
		Kind:                   string(rec.Kind),
		Target:                 string(rec.Target),
		Actor:                  string(rec.Actor),
		State:                  string(rec.State),
		RequestedClass:         string(rec.RequestedClass),
		EffectiveClass:         string(rec.EffectiveClass),
		Fingerprint:            string(rec.Fingerprint),
		IdempotencyFingerprint: string(rec.IdempotencyFingerprint),
		IdempotencyKey:         rec.IdempotencyKey,
		CreatedAt:              rec.CreatedAt.UTC().Format(time.RFC3339Nano),
		ReceiptID:              string(rec.ReceiptID),
		ErrorCategory:          rec.ErrorCategory,
		ErrorMessage:           rec.ErrorMessage,
		Parameters:             rec.Parameters,
		ApprovalID:             string(rec.ApprovalID),
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
