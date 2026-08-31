package mcpadapter

import "github.com/Horcag/agent-machine-control/internal/client"

// Re-export shared DTOs
type SessionDTO = client.SessionDTO
type SessionChunkDTO = client.SessionChunkDTO

type SessionOpenInput struct {
	Target         string `json:"target,omitempty" jsonschema:"Optional enrolled target reference (default, alias, provider GUID, or canonical locator)"`
	Reason         string `json:"reason" jsonschema:"Reason for opening terminal session"`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Idempotency key"`
	Timeout        string `json:"timeout" jsonschema:"Operation timeout (e.g. 30s, 5m)"`
	ApprovalID     string `json:"approval_id,omitempty" jsonschema:"Server-issued approval reference for destructive or privileged execution"`
	Deadline       string `json:"deadline,omitempty" jsonschema:"Exact canonical RFC3339Nano deadline returned with approval_id"`
	Cols           uint16 `json:"cols,omitempty" jsonschema:"Terminal columns (20-500, default 80)"`
	Rows           uint16 `json:"rows,omitempty" jsonschema:"Terminal rows (5-200, default 24)"`
	Term           string `json:"term,omitempty" jsonschema:"Terminal emulation type (default xterm-256color)"`
}

type SessionOpenResult struct {
	SchemaVersion   string      `json:"schema_version"`
	ObservationType string      `json:"observation_type"`
	Session         SessionDTO  `json:"session"`
	Receipt         *ReceiptDTO `json:"receipt,omitempty"`
}

type SessionReadInput struct {
	SessionID string `json:"session_id" jsonschema:"Session ID (sess-...)"`
	AfterSeq  uint64 `json:"after_seq,omitempty" jsonschema:"Read chunks strictly after sequence number"`
	Limit     int    `json:"limit,omitempty" jsonschema:"Maximum bytes to read (default 65536)"`
	Timeout   string `json:"timeout,omitempty" jsonschema:"Timeout duration"`
}

type SessionReadResult struct {
	SchemaVersion string            `json:"schema_version"`
	SessionID     string            `json:"session_id"`
	Chunks        []SessionChunkDTO `json:"chunks"`
	NextSeq       uint64            `json:"next_seq"`
	LossBytes     uint64            `json:"loss_bytes"`
	HasMore       bool              `json:"has_more"`
	Closed        bool              `json:"closed"`
	ExitCode      *int              `json:"exit_code,omitempty"`
}

type SessionWriteInput struct {
	SessionID      string `json:"session_id" jsonschema:"Session ID"`
	Data           string `json:"data" jsonschema:"Character data or command to write"`
	Reason         string `json:"reason" jsonschema:"Reason for writing data"`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Idempotency key"`
	Timeout        string `json:"timeout" jsonschema:"Operation timeout (e.g. 30s, 5m)"`
	ApprovalID     string `json:"approval_id,omitempty" jsonschema:"Server-issued approval reference for destructive or privileged execution"`
	Deadline       string `json:"deadline,omitempty" jsonschema:"Exact canonical RFC3339Nano deadline returned with approval_id"`
}

type SessionWriteResult struct {
	SchemaVersion string      `json:"schema_version"`
	BytesWritten  int         `json:"bytes_written"`
	Receipt       *ReceiptDTO `json:"receipt,omitempty"`
}

type SessionControlInput struct {
	SessionID      string `json:"session_id" jsonschema:"Session ID"`
	Key            string `json:"key" jsonschema:"Control key name (ctrl-c, ctrl-d, enter, tab, etc.)"`
	Reason         string `json:"reason" jsonschema:"Reason for control keystroke"`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Idempotency key"`
	Timeout        string `json:"timeout" jsonschema:"Operation timeout (e.g. 30s, 5m)"`
	ApprovalID     string `json:"approval_id,omitempty" jsonschema:"Server-issued approval reference for destructive or privileged execution"`
	Deadline       string `json:"deadline,omitempty" jsonschema:"Exact canonical RFC3339Nano deadline returned with approval_id"`
}

type SessionControlResult struct {
	SchemaVersion string      `json:"schema_version"`
	Status        string      `json:"status"`
	Receipt       *ReceiptDTO `json:"receipt,omitempty"`
}

type SessionWaitInput struct {
	SessionID string `json:"session_id" jsonschema:"Session ID"`
	SettleMs  int    `json:"settle_ms,omitempty" jsonschema:"Quiet settle time (default 500)"`
	Regex     string `json:"regex,omitempty" jsonschema:"Regular expression pattern to match"`
	AfterSeq  uint64 `json:"after_seq,omitempty" jsonschema:"Sequence number cursor"`
	Timeout   string `json:"timeout,omitempty" jsonschema:"Timeout duration"`
}

type SessionWaitResult struct {
	SchemaVersion string            `json:"schema_version"`
	SessionID     string            `json:"session_id"`
	Chunks        []SessionChunkDTO `json:"chunks"`
	NextSeq       uint64            `json:"next_seq"`
	LossBytes     uint64            `json:"loss_bytes"`
	Matched       bool              `json:"matched"`
	Closed        bool              `json:"closed"`
}

type SessionListInput struct {
	Machine string `json:"machine,omitempty" jsonschema:"Filter by virtual machine GUID"`
}

type SessionListResult struct {
	SchemaVersion string       `json:"schema_version"`
	Sessions      []SessionDTO `json:"sessions"`
}

type SessionShowInput struct {
	SessionID string `json:"session_id" jsonschema:"Session ID"`
}

type SessionShowResult struct {
	SchemaVersion   string     `json:"schema_version"`
	ObservationType string     `json:"observation_type"`
	Session         SessionDTO `json:"session"`
}

type SessionCloseInput struct {
	SessionID      string `json:"session_id" jsonschema:"Session ID"`
	Reason         string `json:"reason" jsonschema:"Reason for closing session"`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Idempotency key"`
	Timeout        string `json:"timeout" jsonschema:"Operation timeout (e.g. 30s, 5m)"`
	ApprovalID     string `json:"approval_id,omitempty" jsonschema:"Server-issued approval reference for destructive or privileged execution"`
	Deadline       string `json:"deadline,omitempty" jsonschema:"Exact canonical RFC3339Nano deadline returned with approval_id"`
}

type SessionCloseResult struct {
	SchemaVersion string      `json:"schema_version"`
	Session       SessionDTO  `json:"session"`
	Receipt       *ReceiptDTO `json:"receipt,omitempty"`
}
