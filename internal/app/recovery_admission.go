package app

import (
	"context"
	"errors"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
)

func (s *RecoveryService) prepareAndAuthorizeMutation(
	ctx context.Context,
	op domain.Operation,
	req MutationRequest,
	providerTargetID string,
	now time.Time,
) (policy.Decision, string, error) {
	if decision, err := s.precheckOperationApprovalReference(ctx, op, req, now); decision != nil || err != nil {
		if decision == nil {
			return policy.Decision{}, "", err
		}
		return *decision, "", err
	}
	rollbackState, rollbackRef, err := s.discoverRollback(ctx, op, providerTargetID)
	if err != nil {
		return policy.Decision{}, rollbackRef, err
	}
	decision, err := s.evaluateMutationPolicy(ctx, op, req, providerTargetID, now, rollbackState)
	if err != nil {
		return decision, rollbackRef, err
	}
	if err := s.verifyApprovalUnconsumed(ctx, decision, req); err != nil {
		return decision, rollbackRef, err
	}
	return decision, rollbackRef, nil
}

func (s *RecoveryService) precheckOperationApprovalReference(ctx context.Context, op domain.Operation, req MutationRequest, now time.Time) (*policy.Decision, error) {
	if req.ApprovalError != nil {
		decision := policy.Decision{Type: policy.DecisionDeny, EffectiveClass: op.Classification}
		return &decision, &PolicyDeniedError{Reason: policy.DenialApprovalMismatch, Message: "server-issued approval reference is invalid"}
	}
	if req.ApprovalID == "" || req.Approval == nil {
		return nil, nil
	}
	decision := policy.Decision{Type: policy.DecisionDeny, EffectiveClass: req.Approval.AuthorizedClass}
	if err := req.Approval.IsActive(now); err != nil {
		return &decision, approvalActivityDenial(err)
	}
	if s.approvalStore == nil {
		return nil, nil
	}
	consumed, err := s.approvalStore.IsConsumedContext(ctx, req.ApprovalID)
	if err != nil {
		return nil, err
	}
	if consumed {
		return &decision, approvalActivityDenial(domain.ErrApprovalConsumed)
	}
	return nil, nil
}

func approvalActivityDenial(err error) error {
	reason := policy.DenialApprovalMismatch
	message := "server-issued approval reference is inactive"
	switch {
	case errors.Is(err, domain.ErrApprovalExpired):
		reason = policy.DenialApprovalExpired
		message = "server-issued approval reference has expired"
	case errors.Is(err, domain.ErrApprovalConsumed):
		reason = policy.DenialApprovalConsumed
		message = "server-issued approval reference has already been consumed"
	case errors.Is(err, domain.ErrApprovalNotYetValid):
		reason = policy.DenialApprovalNotYetValid
	}
	return errors.Join(err, &PolicyDeniedError{Reason: reason, Message: message})
}

func runLifecycleHooks(ctx context.Context, req MutationRequest) error {
	if req.OnAdmitted != nil {
		if err := req.OnAdmitted(ctx); err != nil {
			return err
		}
	}
	if req.OnRunning != nil {
		if err := req.OnRunning(ctx); err != nil {
			return err
		}
	}
	return nil
}
