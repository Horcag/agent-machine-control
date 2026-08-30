package daemon

import (
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

// SessionDTO is the API representation of a persistent terminal session.
type SessionDTO struct {
	SessionID       string  `json:"session_id"`
	Target          string  `json:"target"`
	OwnerActor      string  `json:"owner_actor"`
	State           string  `json:"state"`
	CreatedAt       string  `json:"created_at"`
	ClosedAt        *string `json:"closed_at,omitempty"`
	LastActivityAt  string  `json:"last_activity_at"`
	BytesRead       uint64  `json:"bytes_read"`
	BytesWritten    uint64  `json:"bytes_written"`
	Cols            uint16  `json:"cols"`
	Rows            uint16  `json:"rows"`
	TermType        string  `json:"term_type"`
	ExitCode        *int    `json:"exit_code,omitempty"`
	ErrorMessage    string  `json:"error_message,omitempty"`
	ObservationType string  `json:"observation_type"`
}

// SessionChunkDTO is the API representation of a sanitized terminal output chunk.
type SessionChunkDTO struct {
	Seq       uint64 `json:"seq"`
	Timestamp string `json:"timestamp"`
	Data      string `json:"data"`
	LossBytes uint64 `json:"loss_bytes,omitempty"`
}

// ConvertToSessionDTO converts a domain.SessionObservation into a SessionDTO.
func ConvertToSessionDTO(s domain.SessionObservation) SessionDTO {
	var closedAt *string
	if s.ClosedAt != nil {
		str := s.ClosedAt.UTC().Format(time.RFC3339)
		closedAt = &str
	}
	return SessionDTO{
		SessionID:       string(s.ID),
		Target:          string(s.Target),
		OwnerActor:      string(s.OwnerActor),
		State:           string(s.State),
		CreatedAt:       s.CreatedAt.UTC().Format(time.RFC3339),
		ClosedAt:        closedAt,
		LastActivityAt:  s.LastActivityAt.UTC().Format(time.RFC3339),
		BytesRead:       s.BytesRead,
		BytesWritten:    s.BytesWritten,
		Cols:            s.Cols,
		Rows:            s.Rows,
		TermType:        s.TermType,
		ExitCode:        s.ExitCode,
		ErrorMessage:    s.ErrorMessage,
		ObservationType: string(s.ObservationType),
	}
}

// ConvertToChunkDTOs converts a slice of domain.SessionChunk into SessionChunkDTOs.
func ConvertToChunkDTOs(chunks []domain.SessionChunk) []SessionChunkDTO {
	if chunks == nil {
		return []SessionChunkDTO{}
	}
	res := make([]SessionChunkDTO, len(chunks))
	for i, c := range chunks {
		res[i] = SessionChunkDTO{
			Seq:       c.Seq,
			Timestamp: c.Timestamp.UTC().Format(time.RFC3339),
			Data:      c.Data,
			LossBytes: c.LossBytes,
		}
	}
	return res
}

// SessionOpenRequest payload for POST /v1/sessions.
type SessionOpenRequest struct {
	Target         string           `json:"target"`
	Reason         string           `json:"reason"`
	IdempotencyKey string           `json:"idempotency_key"`
	Cols           uint16           `json:"cols,omitempty"`
	Rows           uint16           `json:"rows,omitempty"`
	Term           string           `json:"term,omitempty"`
	TimeoutSeconds int64            `json:"timeout_seconds,omitempty"`
	TimeoutMillis  int64            `json:"timeout_ms,omitempty"`
	Approval       *domain.Approval `json:"approval,omitempty"`
}

// SessionOpenResponse payload returned by POST /v1/sessions.
type SessionOpenResponse struct {
	SchemaVersion string       `json:"schema_version"`
	Session       SessionDTO   `json:"session"`
	Receipt       *receipt.DTO `json:"receipt,omitempty"`
}

// SessionReadResponse payload returned by GET /v1/sessions/{id}/read.
type SessionReadResponse struct {
	SchemaVersion string            `json:"schema_version"`
	SessionID     string            `json:"session_id"`
	Chunks        []SessionChunkDTO `json:"chunks"`
	NextSeq       uint64            `json:"next_seq"`
	LossBytes     uint64            `json:"loss_bytes"`
	HasMore       bool              `json:"has_more"`
	Closed        bool              `json:"closed"`
	ExitCode      *int              `json:"exit_code,omitempty"`
}

// SessionWriteRequest payload for POST /v1/sessions/{id}/write.
type SessionWriteRequest struct {
	Data           string           `json:"data"`
	Reason         string           `json:"reason"`
	IdempotencyKey string           `json:"idempotency_key"`
	TimeoutSeconds int64            `json:"timeout_seconds,omitempty"`
	TimeoutMillis  int64            `json:"timeout_ms,omitempty"`
	Approval       *domain.Approval `json:"approval,omitempty"`
}

// SessionWriteResponse payload returned by POST /v1/sessions/{id}/write.
type SessionWriteResponse struct {
	SchemaVersion string       `json:"schema_version"`
	BytesWritten  int          `json:"bytes_written"`
	Receipt       *receipt.DTO `json:"receipt,omitempty"`
}

// SessionControlRequest payload for POST /v1/sessions/{id}/control.
type SessionControlRequest struct {
	Key            string           `json:"key"`
	Reason         string           `json:"reason"`
	IdempotencyKey string           `json:"idempotency_key"`
	TimeoutSeconds int64            `json:"timeout_seconds,omitempty"`
	TimeoutMillis  int64            `json:"timeout_ms,omitempty"`
	Approval       *domain.Approval `json:"approval,omitempty"`
}

// SessionControlResponse payload returned by POST /v1/sessions/{id}/control.
type SessionControlResponse struct {
	SchemaVersion string       `json:"schema_version"`
	Status        string       `json:"status"`
	Receipt       *receipt.DTO `json:"receipt,omitempty"`
}

// SessionWaitRequest payload for POST /v1/sessions/{id}/wait.
type SessionWaitRequest struct {
	SettleMs       int    `json:"settle_ms,omitempty"`
	Regex          string `json:"regex,omitempty"`
	AfterSeq       uint64 `json:"after_seq,omitempty"`
	TimeoutSeconds int64  `json:"timeout_seconds,omitempty"`
	TimeoutMillis  int64  `json:"timeout_ms,omitempty"`
}

// SessionWaitResponse payload returned by POST /v1/sessions/{id}/wait.
type SessionWaitResponse struct {
	SchemaVersion string            `json:"schema_version"`
	SessionID     string            `json:"session_id"`
	Chunks        []SessionChunkDTO `json:"chunks"`
	NextSeq       uint64            `json:"next_seq"`
	LossBytes     uint64            `json:"loss_bytes"`
	Matched       bool              `json:"matched"`
	Closed        bool              `json:"closed"`
}

// SessionListResponse payload returned by GET /v1/sessions.
type SessionListResponse struct {
	SchemaVersion string       `json:"schema_version"`
	Sessions      []SessionDTO `json:"sessions"`
}

// SessionCloseRequest payload for POST /v1/sessions/{id}/close.
type SessionCloseRequest struct {
	Reason         string           `json:"reason"`
	IdempotencyKey string           `json:"idempotency_key"`
	TimeoutSeconds int64            `json:"timeout_seconds,omitempty"`
	TimeoutMillis  int64            `json:"timeout_ms,omitempty"`
	Force          bool             `json:"force,omitempty"`
	Approval       *domain.Approval `json:"approval,omitempty"`
}

// SessionCloseResponse payload returned by POST /v1/sessions/{id}/close.
type SessionCloseResponse struct {
	SchemaVersion string       `json:"schema_version"`
	Session       SessionDTO   `json:"session"`
	Receipt       *receipt.DTO `json:"receipt,omitempty"`
}
