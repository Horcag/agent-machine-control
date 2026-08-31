package app

import (
	"context"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func buildOperationApprovalIssuanceEvidence(caller domain.ActorContext, approved domain.Operation, issued domain.Approval) (domain.Operation, domain.Receipt, error) {
	deadlineText := issued.ExpiresAt.UTC().Format(time.RFC3339Nano)
	op := domain.Operation{
		Kind: "operation.approval.issue", Target: approved.Target, Actor: caller,
		Reason: approved.Reason, Deadline: issued.ExpiresAt,
		IdempotencyKey:     domain.DeriveApprovalIdempotencyKey("operation-approval-issue:" + string(issued.ID)),
		RequiredCapability: "operation.approval.issue", RequiredScopes: []string{domain.ScopeOperationAdmin},
		Classification: domain.ClassDestructivePrivileged, EvidenceSensitivity: domain.EvidenceSensitivityStandard,
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
	rcpt, err := buildApprovalIssuanceReceipt("operation-approval-receipt", op, issued)
	if err != nil {
		return domain.Operation{}, domain.Receipt{}, err
	}
	return op, rcpt, nil
}

func (s *RecoveryService) persistIssuedOperationApproval(ctx context.Context, existing *domain.Approval, issued domain.Approval, issuanceOp domain.Operation, issuanceReceipt domain.Receipt) error {
	if existing == nil {
		if err := s.auditStore.RecordAdmissionIntentContext(ctx, issuanceOp); err != nil {
			return err
		}
		if err := s.approvalStore.IssueContext(ctx, issued); err != nil {
			return err
		}
	}
	if err := s.receiptStore.EnsureContext(ctx, issuanceReceipt); err != nil {
		return err
	}
	return s.auditStore.EnsureTerminalOutcomeContext(ctx, issuanceReceipt)
}

func operationApprovalGrant(issued domain.Approval, approved domain.Operation) *OperationApprovalGrant {
	return &OperationApprovalGrant{
		ApprovalID: string(issued.ID), Deadline: approved.Deadline.UTC(), ExpiresAt: issued.ExpiresAt.UTC(),
		Operation: OperationApprovalSummary{
			Kind: approved.Kind, Target: approved.Target, Reason: approved.Reason,
			IdempotencyKey: approved.IdempotencyKey, Parameters: domain.DeepCloneMap(approved.Parameters),
		},
	}
}

// LoadOperationApprovalReference resolves immutable authority after the exact operation is canonical.
func (s *RecoveryService) LoadOperationApprovalReference(ctx context.Context, op domain.Operation, approvalID string) (*domain.Approval, error) {
	if s == nil || s.approvalStore == nil || approvalID == "" {
		return nil, ErrInvalidOperationApprovalReference
	}
	if err := domain.ValidateApprovalID(approvalID); err != nil {
		return nil, ErrInvalidOperationApprovalReference
	}
	loaded, err := s.approvalStore.LoadIssuedContext(ctx, approvalID)
	if err != nil || loaded == nil || loaded.ID.String() != approvalID || !loaded.ExpiresAt.Equal(op.Deadline) {
		return nil, ErrInvalidOperationApprovalReference
	}
	if err := loaded.Matches(op); err != nil {
		return nil, ErrInvalidOperationApprovalReference
	}
	return loaded, nil
}
