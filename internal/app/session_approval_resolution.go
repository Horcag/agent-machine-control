package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
)

func validateSessionApprovalInput(approval *domain.Approval, approvalID string) error {
	if approval != nil && approvalID != "" {
		return fmt.Errorf("%w: approval and approval_id are mutually exclusive", domain.ErrInvalidApprovalRecord)
	}
	if approvalID == "" {
		return nil
	}
	return domain.ValidateApprovalID(approvalID)
}

func (s *SessionService) loadSessionApprovalReference(ctx context.Context, approvalID string) (*domain.Approval, error) {
	if approvalID == "" {
		return nil, nil
	}
	if s.approvalStore == nil {
		return nil, invalidSessionApprovalReference()
	}
	loaded, err := s.approvalStore.LoadIssuedContext(ctx, approvalID)
	if err != nil {
		return nil, invalidSessionApprovalReference()
	}
	return loaded, nil
}

func invalidSessionApprovalReference() error {
	return &PolicyDeniedError{
		Reason:  policy.DenialApprovalMismatch,
		Message: "server-issued approval reference is invalid",
	}
}

func (s *SessionService) resolveSessionMutationApproval(
	ctx context.Context,
	op domain.Operation,
	approval *domain.Approval,
	approvalID string,
) (*domain.Approval, *domain.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if approval != nil && !op.Actor.HasScope(domain.ScopeSessionAdmin) {
		denial := &PolicyDeniedError{
			Reason:  policy.DenialApprovalRequired,
			Message: "raw approval objects require an authenticated session administrator",
		}
		return s.persistSessionApprovalDenial(ctx, op, denial)
	}
	loaded, err := s.loadSessionApprovalReference(ctx, approvalID)
	if err != nil {
		return s.persistSessionApprovalDenial(ctx, op, err)
	}
	if loaded != nil {
		return loaded, nil, nil
	}
	return approval, nil, nil
}

func (s *SessionService) persistSessionApprovalDenial(ctx context.Context, op domain.Operation, denial error) (*domain.Approval, *domain.Receipt, error) {
	op.Classification = s.resolveSafety(ctx, op.Target).Classification
	fp, idFp, err := s.validateAndFingerprint(op)
	if err != nil {
		return nil, nil, errors.Join(denial, err)
	}
	now := s.now()
	decision := policy.Decision{Type: policy.DecisionDeny, EffectiveClass: op.Classification}
	rcpt, persistErr := s.persistOutcome(ctx, op, fp, idFp, decision, now, now, denial, "", nil, 7, false)
	return nil, &rcpt, errors.Join(denial, persistErr)
}
