package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

const (
	operationApprovalMCPBeneficiary domain.ActorID = "agent:mcp-local"
	minOperationApprovalValidity                   = time.Second
	maxOperationApprovalValidity                   = 5 * time.Minute
)

var (
	// ErrOperationApprovalNotRequired indicates the exact current operation is not privileged.
	ErrOperationApprovalNotRequired = errors.New("app: operation approval is not required")
	// ErrOperationApprovalForbidden indicates the caller cannot issue the requested authority.
	ErrOperationApprovalForbidden = errors.New("app: operation approval issuance is forbidden")
	// ErrInvalidOperationApprovalReference is a sanitized server-owned reference failure.
	ErrInvalidOperationApprovalReference = errors.New("app: invalid server-issued operation approval reference")
)

// OperationApprovalIssueParams describes one exact machine/checkpoint approval request.
type OperationApprovalIssueParams struct {
	Kind           domain.OperationKind
	Caller         domain.ActorContext
	Target         string
	Reason         string
	IdempotencyKey string
	ValidFor       time.Duration
	Beneficiary    string
	Parameters     map[string]any
}

// OperationApprovalSummary is the redacted operation identity returned to the operator.
type OperationApprovalSummary struct {
	Kind           domain.OperationKind `json:"kind"`
	Target         domain.MachineRef    `json:"target"`
	Reason         string               `json:"reason"`
	IdempotencyKey string               `json:"idempotency_key"`
	Parameters     map[string]any       `json:"parameters"`
}

// OperationApprovalGrant contains only a copy-safe reference and exact deadline.
type OperationApprovalGrant struct {
	ApprovalID string                   `json:"approval_id"`
	Deadline   time.Time                `json:"deadline"`
	ExpiresAt  time.Time                `json:"expires_at"`
	Operation  OperationApprovalSummary `json:"operation"`
}

// IssueOperationApproval issues or idempotently returns immutable server-owned authority.
func (s *RecoveryService) IssueOperationApproval(ctx context.Context, params OperationApprovalIssueParams) (*OperationApprovalGrant, *domain.Receipt, error) {
	beneficiary, err := validateOperationApprovalIssue(params)
	if err != nil {
		return nil, nil, err
	}
	if s == nil || s.approvalStore == nil || s.auditStore == nil || s.receiptStore == nil || s.backend == nil {
		return nil, nil, errors.New("app: operation approval persistence is unavailable")
	}

	s.issueMu.Lock()
	defer s.issueMu.Unlock()

	existing, err := s.loadIssuedOperationApproval(ctx, beneficiary, params.IdempotencyKey)
	if err != nil {
		return nil, nil, err
	}
	issuedAt, deadline := s.operationApprovalTimes(params.ValidFor, existing)
	approvedOp, effectiveClass, err := s.prepareOperationApproval(ctx, params, beneficiary, deadline)
	if err != nil {
		return nil, nil, err
	}
	fingerprint, err := approvedOp.Fingerprint()
	if err != nil {
		return nil, nil, err
	}
	issued := domain.Approval{
		ID: deriveOperationApprovalID(beneficiary, params.IdempotencyKey), Actor: beneficiary,
		Target: approvedOp.Target, AuthorizedClass: effectiveClass, Fingerprint: fingerprint,
		IdempotencyKey: approvedOp.IdempotencyKey, IssuedAt: issuedAt, ExpiresAt: deadline,
	}
	if existing != nil && !equalIssuedOperationApproval(*existing, issued) {
		return nil, nil, receipt.ErrIdempotencyCollision
	}

	issuanceOp, issuanceReceipt, err := buildOperationApprovalIssuanceEvidence(params.Caller, approvedOp, issued)
	if err != nil {
		return nil, nil, err
	}
	if err := s.persistIssuedOperationApproval(ctx, existing, issued, issuanceOp, issuanceReceipt); err != nil {
		return nil, &issuanceReceipt, err
	}
	return operationApprovalGrant(issued, approvedOp), &issuanceReceipt, nil
}

func validateOperationApprovalIssue(params OperationApprovalIssueParams) (domain.ActorID, error) {
	if err := params.Caller.Validate(); err != nil {
		return "", err
	}
	if params.Caller.IsDelegated() || !params.Caller.HasScope(domain.ScopeOperationAdmin) {
		return "", ErrOperationApprovalForbidden
	}
	if params.ValidFor < minOperationApprovalValidity || params.ValidFor > maxOperationApprovalValidity {
		return "", fmt.Errorf("operation approval validity must be between %s and %s", minOperationApprovalValidity, maxOperationApprovalValidity)
	}
	if err := domain.ValidateReason(params.Reason); err != nil {
		return "", err
	}
	if err := domain.ValidateIdempotencyKey(params.IdempotencyKey); err != nil {
		return "", err
	}
	if err := domain.ValidateOperationParameters(params.Kind, params.Parameters); err != nil {
		return "", err
	}
	switch params.Kind {
	case "machine.start", "machine.stop", "checkpoint.create", "checkpoint.restore":
	default:
		return "", domain.ErrInvalidOperationKind
	}
	switch params.Beneficiary {
	case "", "self":
		return params.Caller.EffectiveActor, nil
	case string(operationApprovalMCPBeneficiary):
		return operationApprovalMCPBeneficiary, nil
	default:
		return "", ErrOperationApprovalForbidden
	}
}

func (s *RecoveryService) loadIssuedOperationApproval(ctx context.Context, beneficiary domain.ActorID, key string) (*domain.Approval, error) {
	loaded, err := s.approvalStore.LoadIssuedContext(ctx, string(deriveOperationApprovalID(beneficiary, key)))
	if errors.Is(err, approval.ErrApprovalNotIssued) {
		return nil, nil
	}
	return loaded, err
}

func (s *RecoveryService) operationApprovalTimes(validFor time.Duration, existing *domain.Approval) (time.Time, time.Time) {
	if existing != nil {
		return existing.IssuedAt, existing.ExpiresAt
	}
	issuedAt := s.now()
	return issuedAt, issuedAt.Add(validFor)
}

func (s *RecoveryService) prepareOperationApproval(ctx context.Context, params OperationApprovalIssueParams, beneficiary domain.ActorID, deadline time.Time) (domain.Operation, domain.OperationClass, error) {
	canonical, providerID, err := s.resolveTargetReference(ctx, params.Target)
	if err != nil {
		return domain.Operation{}, "", err
	}
	actor, err := operationApprovalBeneficiaryActor(params.Caller, beneficiary)
	if err != nil {
		return domain.Operation{}, "", err
	}
	initialClass, capability, err := operationApprovalContract(params.Kind, params.Parameters)
	if err != nil {
		return domain.Operation{}, "", err
	}
	op, err := s.buildOperation(params.Kind, MutationRequest{
		TargetID: canonical, Actor: actor, Reason: params.Reason,
		IdempotencyKey: params.IdempotencyKey, Deadline: deadline,
	}, initialClass, capability, domain.DeepCloneMap(params.Parameters))
	if err != nil {
		return domain.Operation{}, "", err
	}
	rollback, _, err := s.discoverRollback(ctx, op, providerID)
	if err != nil {
		return domain.Operation{}, "", err
	}
	caps, err := s.backend.Capabilities(ctx, providerID)
	if err != nil {
		return domain.Operation{}, "", fmt.Errorf("app: failed to retrieve backend capabilities: %w", err)
	}
	auditWritable := s.auditStore.CheckWritableContext(ctx) == nil
	if err := ctx.Err(); err != nil {
		return domain.Operation{}, "", err
	}
	decision := policy.Evaluate(policy.EvaluationInput{
		Operation: op, Now: s.now(), AuditWritable: auditWritable, RollbackState: rollback,
		RollbackPolicy:        policy.RollbackPolicyEscalateToDestructive,
		AvailableCapabilities: caps, SensitiveEvidenceScopes: domain.NewScopeSet("evidence:sensitive"),
	})
	if decision.Type == policy.DecisionAllow {
		return domain.Operation{}, "", ErrOperationApprovalNotRequired
	}
	if decision.DenialReason != policy.DenialApprovalRequired {
		return domain.Operation{}, "", &PolicyDeniedError{Reason: decision.DenialReason, Message: decision.DenialMessage}
	}
	return op, decision.EffectiveClass, nil
}

func operationApprovalBeneficiaryActor(caller domain.ActorContext, beneficiary domain.ActorID) (domain.ActorContext, error) {
	if beneficiary == caller.EffectiveActor {
		return caller.Clone(), nil
	}
	if beneficiary != operationApprovalMCPBeneficiary {
		return domain.ActorContext{}, ErrOperationApprovalForbidden
	}
	scopes := domain.NewScopeSet(domain.ScopeMachineRead, domain.ScopeMachineWrite)
	return domain.NewActorContext(beneficiary, beneficiary, scopes, scopes)
}

func operationApprovalContract(kind domain.OperationKind, params map[string]any) (domain.OperationClass, domain.Capability, error) {
	switch kind {
	case "machine.start":
		return domain.ClassReversibleMutation, domain.CapabilityMachineStart, nil
	case "machine.stop":
		class := domain.ClassReversibleMutation
		if params != nil && params["mode"] == "turn-off" {
			class = domain.ClassDestructivePrivileged
		}
		return class, domain.CapabilityMachineStop, nil
	case "checkpoint.create":
		return domain.ClassDestructivePrivileged, domain.CapabilityCheckpointCreate, nil
	case "checkpoint.restore":
		return domain.ClassDestructivePrivileged, domain.CapabilityCheckpointRestore, nil
	default:
		return "", "", domain.ErrInvalidOperationKind
	}
}

func deriveOperationApprovalID(beneficiary domain.ActorID, key string) domain.ApprovalID {
	digest := sha256.Sum256([]byte("operation\x00" + string(beneficiary) + "\x00" + key))
	return domain.ApprovalID("app-operation-" + hex.EncodeToString(digest[:16]))
}

func equalIssuedOperationApproval(left, right domain.Approval) bool {
	return left.ID == right.ID && left.Actor == right.Actor && left.Target == right.Target &&
		left.AuthorizedClass == right.AuthorizedClass && left.Fingerprint == right.Fingerprint &&
		left.IdempotencyKey == right.IdempotencyKey && left.IssuedAt.Equal(right.IssuedAt) &&
		left.ExpiresAt.Equal(right.ExpiresAt) && !left.Consumed && left.ConsumedAt == nil
}
