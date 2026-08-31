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
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

const (
	minTargetApprovalValidity = time.Second
	maxTargetApprovalValidity = 5 * time.Minute
)

// TargetApprovalIssueParams describes one exact operator target-authority approval request.
type TargetApprovalIssueParams struct {
	Kind           domain.OperationKind
	Reference      string
	Aliases        []string
	Caller         domain.ActorContext
	Reason         string
	IdempotencyKey string
	ValidFor       time.Duration
}

// TargetApprovalSummary is the redacted immutable operation approved by the server.
type TargetApprovalSummary struct {
	Kind           domain.OperationKind `json:"kind"`
	Target         domain.MachineRef    `json:"target"`
	Reason         string               `json:"reason"`
	IdempotencyKey string               `json:"idempotency_key"`
	Parameters     map[string]any       `json:"parameters"`
}

// TargetApprovalGrant is the public issuance result.
type TargetApprovalGrant struct {
	ApprovalID string                `json:"approval_id"`
	Deadline   time.Time             `json:"deadline"`
	ExpiresAt  time.Time             `json:"expires_at"`
	Operation  TargetApprovalSummary `json:"operation"`
	Receipt    domain.Receipt        `json:"receipt"`
}

// IssueApproval issues or exactly replays one operator-bound target approval.
func (c *TargetCoordinator) IssueApproval(ctx context.Context, params TargetApprovalIssueParams) (*TargetApprovalGrant, error) {
	if err := validateTargetOperator(params.Caller); err != nil {
		return nil, err
	}
	if params.ValidFor < minTargetApprovalValidity || params.ValidFor > maxTargetApprovalValidity {
		return nil, fmt.Errorf("target approval validity must be between %s and %s", minTargetApprovalValidity, maxTargetApprovalValidity)
	}
	approvalID := targetApprovalID(params.Caller.EffectiveActor, params.IdempotencyKey)
	existing, issuedAt, deadline, err := c.loadTargetApproval(ctx, approvalID, params.ValidFor)
	if err != nil {
		return nil, err
	}
	plan, err := c.prepareMutationPlan(ctx, TargetMutationParams{
		Kind: params.Kind, Reference: params.Reference, Aliases: params.Aliases,
		Caller: params.Caller, Reason: params.Reason, IdempotencyKey: params.IdempotencyKey, Deadline: deadline,
	}, nil)
	if err != nil {
		return nil, err
	}
	approvedOp, err := buildTargetOperation(plan, params.Caller, params.Reason, params.IdempotencyKey, deadline)
	if err != nil {
		return nil, err
	}
	fingerprint, err := approvedOp.Fingerprint()
	if err != nil {
		return nil, err
	}
	issued := domain.Approval{
		ID: domain.ApprovalID(approvalID), Actor: approvedOp.Actor.EffectiveActor, Target: approvedOp.Target,
		AuthorizedClass: domain.ClassDestructivePrivileged, Fingerprint: fingerprint,
		IdempotencyKey: approvedOp.IdempotencyKey, IssuedAt: issuedAt, ExpiresAt: deadline,
	}
	issuanceOp, issuanceReceipt, err := buildTargetApprovalIssuanceEvidence(params.Caller, approvedOp, issued)
	if err != nil {
		return nil, err
	}
	if err := c.persistTargetApproval(ctx, existing, issued, issuanceOp, issuanceReceipt); err != nil {
		return nil, err
	}
	return &TargetApprovalGrant{
		ApprovalID: approvalID, Deadline: deadline.UTC(), ExpiresAt: deadline.UTC(), Receipt: issuanceReceipt,
		Operation: TargetApprovalSummary{
			Kind: approvedOp.Kind, Target: approvedOp.Target, Reason: approvedOp.Reason,
			IdempotencyKey: approvedOp.IdempotencyKey, Parameters: domain.DeepCloneMap(approvedOp.Parameters),
		},
	}, nil
}

func (c *TargetCoordinator) loadTargetApproval(ctx context.Context, approvalID string, validFor time.Duration) (*domain.Approval, time.Time, time.Time, error) {
	existing, err := c.approvalStore.LoadIssuedContext(ctx, approvalID)
	if err != nil && !errors.Is(err, approval.ErrApprovalNotIssued) {
		return nil, time.Time{}, time.Time{}, err
	}
	issuedAt := c.now()
	deadline := issuedAt.Add(validFor)
	if existing != nil {
		issuedAt = existing.IssuedAt
		deadline = existing.ExpiresAt
	}
	return existing, issuedAt, deadline, nil
}

func (c *TargetCoordinator) persistTargetApproval(
	ctx context.Context,
	existing *domain.Approval,
	issued domain.Approval,
	issuanceOp domain.Operation,
	issuanceReceipt domain.Receipt,
) error {
	if existing != nil && targetApprovalCollision(*existing, issued) {
		return receipt.ErrIdempotencyCollision
	}
	if existing == nil {
		if err := c.auditStore.RecordAdmissionIntentContext(ctx, issuanceOp); err != nil {
			return err
		}
		if err := c.approvalStore.IssueContext(ctx, issued); err != nil {
			return err
		}
	}
	if err := c.receiptStore.EnsureContext(ctx, issuanceReceipt); err != nil {
		return err
	}
	return c.auditStore.EnsureTerminalOutcomeContext(ctx, issuanceReceipt)
}

func buildTargetApprovalIssuanceEvidence(caller domain.ActorContext, approved domain.Operation, issued domain.Approval) (domain.Operation, domain.Receipt, error) {
	deadlineText := issued.ExpiresAt.UTC().Format(time.RFC3339Nano)
	op := domain.Operation{
		Kind: "target.approval.issue", Target: approved.Target, Actor: caller,
		Reason: approved.Reason, Deadline: issued.ExpiresAt,
		IdempotencyKey: domain.DeriveApprovalIdempotencyKey("target-approval-issue:" + approved.IdempotencyKey),
		RequiredScopes: []string{domain.ScopeTargetAdmin}, Classification: domain.ClassDestructivePrivileged,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters: map[string]any{
			"approval_id": string(issued.ID), "approved_fingerprint": string(issued.Fingerprint),
			"approved_kind": string(approved.Kind), "beneficiary": string(issued.Actor), "deadline": deadlineText,
		},
	}
	if err := op.Validate(); err != nil {
		return domain.Operation{}, domain.Receipt{}, err
	}
	if err := domain.ValidateOperationParameters(op.Kind, op.Parameters); err != nil {
		return domain.Operation{}, domain.Receipt{}, err
	}
	fingerprint, err := op.Fingerprint()
	if err != nil {
		return domain.Operation{}, domain.Receipt{}, err
	}
	idempotencyFingerprint, err := domain.ComputeIdempotencyFingerprint(op)
	if err != nil {
		return domain.Operation{}, domain.Receipt{}, err
	}
	digest := sha256.Sum256([]byte("target-approval\x00" + string(issued.ID)))
	receiptValue := domain.Receipt{
		ReceiptID: domain.ReceiptID("rcpt-" + hex.EncodeToString(digest[:16])), OperationKind: op.Kind,
		Fingerprint: fingerprint, IdempotencyFingerprint: idempotencyFingerprint,
		IdempotencyKey: op.IdempotencyKey, Actor: caller.EffectiveActor, Target: op.Target,
		Class: domain.ClassDestructivePrivileged, EffectiveBackend: "control-plane",
		StartedAt: issued.IssuedAt, CompletedAt: issued.IssuedAt,
		Outcome:         domain.ExecutionOutcome{Status: domain.OutcomeSuccess, ExitCode: 0},
		ObservationType: domain.ObservationObserved, EvidenceRefs: []string{string(issued.ID)},
		RedactionStatus: domain.RedactionApplied,
	}
	if err := receiptValue.Validate(); err != nil {
		return domain.Operation{}, domain.Receipt{}, err
	}
	return op, receiptValue, nil
}
