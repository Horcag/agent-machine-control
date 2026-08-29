package cli

import (
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

// OperationOutputEnvelope represents the JSON envelope for single operation responses.
type OperationOutputEnvelope struct {
	SchemaVersion string               `json:"schema_version"`
	Operation     *daemon.OperationDTO `json:"operation"`
}

// OperationListOutputEnvelope represents the JSON envelope for operation list responses.
type OperationListOutputEnvelope struct {
	SchemaVersion string                `json:"schema_version"`
	Operations    []daemon.OperationDTO `json:"operations"`
}

// AuditTailOutputEnvelope represents the JSON envelope for audit tail responses.
type AuditTailOutputEnvelope struct {
	SchemaVersion string        `json:"schema_version"`
	Events        []audit.Event `json:"events"`
}

// ReceiptOutputEnvelope represents the JSON envelope for receipt show responses.
type ReceiptOutputEnvelope struct {
	SchemaVersion string      `json:"schema_version"`
	Receipt       receipt.DTO `json:"receipt"`
}
