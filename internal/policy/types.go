// Package policy implements pure, capability-sensitive and context-sensitive
// policy evaluation for Agent Machine Control operations.
package policy

import (
	"github.com/Horcag/agent-machine-control/internal/domain"
)

// DecisionType represents the result of policy evaluation.
type DecisionType string

const (
	// DecisionAllow permits the operation to proceed to admission.
	DecisionAllow DecisionType = "allow"
	// DecisionDeny rejects the operation before admission.
	DecisionDeny DecisionType = "deny"
)

// DenialReason defines machine-readable structured denial codes.
type DenialReason string

const (
	DenialNone                          DenialReason = ""
	DenialForbidden                     DenialReason = "forbidden_operation"
	DenialUnauthenticated               DenialReason = "unauthenticated_caller"
	DenialInvalidActor                  DenialReason = "invalid_actor"
	DenialDelegationExceeded            DenialReason = "delegation_exceeds_authority"
	DenialDeadlinePassed                DenialReason = "deadline_passed"
	DenialMissingScope                  DenialReason = "missing_required_scope"
	DenialMissingSensitiveEvidenceScope DenialReason = "missing_sensitive_evidence_scope"
	DenialMissingCapability             DenialReason = "missing_required_capability"
	DenialMissingIdempotencyKey         DenialReason = "missing_idempotency_key"
	DenialMissingReason                 DenialReason = "missing_mutation_reason"
	DenialAuditUnwritable               DenialReason = "audit_storage_unwritable"
	DenialRollbackMissing               DenialReason = "missing_verified_rollback_point"
	DenialApprovalRequired              DenialReason = "approval_required"
	DenialApprovalExpired               DenialReason = "approval_record_expired"
	DenialApprovalNotYetValid           DenialReason = "approval_record_not_yet_valid"
	DenialApprovalConsumed              DenialReason = "approval_record_consumed"
	DenialApprovalMismatch              DenialReason = "approval_record_mismatch"
	DenialInvalidOperation              DenialReason = "invalid_operation_structure"
)

// Decision represents the evaluation outcome.
type Decision struct {
	Type                 DecisionType
	EffectiveClass       domain.OperationClass
	Reclassified         bool
	DenialReason         DenialReason
	DenialMessage        string
	OperationFingerprint domain.Fingerprint
	RollbackCheckpointID string
}

// IsAllowed returns true if the decision permits execution.
func (d Decision) IsAllowed() bool {
	return d.Type == DecisionAllow
}

// IsDenied returns true if the decision denies execution.
func (d Decision) IsDenied() bool {
	return d.Type == DecisionDeny
}
