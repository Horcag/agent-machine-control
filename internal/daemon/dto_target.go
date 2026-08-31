package daemon

import "github.com/Horcag/agent-machine-control/internal/receipt"

// TargetApprovalIssueRequest prepares one exact target authority transition for operator approval.
type TargetApprovalIssueRequest struct {
	Kind           string   `json:"kind"`
	Reference      string   `json:"reference,omitempty"`
	Aliases        []string `json:"aliases,omitempty"`
	Reason         string   `json:"reason"`
	IdempotencyKey string   `json:"idempotency_key"`
	ValidForMillis int64    `json:"valid_for_ms"`
}

// TargetMutationRequest executes one exact server-approved target authority transition.
type TargetMutationRequest struct {
	Reference      string   `json:"reference,omitempty"`
	Aliases        []string `json:"aliases,omitempty"`
	Reason         string   `json:"reason"`
	IdempotencyKey string   `json:"idempotency_key"`
	ApprovalID     string   `json:"approval_id"`
	Deadline       string   `json:"deadline"`
}

// TargetDTO exposes only canonical target identity and the provider boundary identifier.
type TargetDTO struct {
	Locator      string `json:"locator"`
	ProviderVMID string `json:"provider_vm_id"`
}

// TargetOperationDTO is the redacted immutable operation identity bound by an approval.
type TargetOperationDTO struct {
	Kind           string         `json:"kind"`
	Target         string         `json:"target"`
	Reason         string         `json:"reason"`
	IdempotencyKey string         `json:"idempotency_key"`
	Parameters     map[string]any `json:"parameters"`
}

// TargetApprovalIssueResponse returns an operator-bound approval reference.
type TargetApprovalIssueResponse struct {
	SchemaVersion string             `json:"schema_version"`
	ApprovalID    string             `json:"approval_id"`
	Deadline      string             `json:"deadline"`
	ExpiresAt     string             `json:"expires_at"`
	Operation     TargetOperationDTO `json:"operation"`
	Receipt       receipt.DTO        `json:"receipt"`
}

// TargetResponse reports fresh canonical target state and optional mutation evidence.
type TargetResponse struct {
	SchemaVersion string       `json:"schema_version"`
	Target        TargetDTO    `json:"target"`
	Receipt       *receipt.DTO `json:"receipt,omitempty"`
}
