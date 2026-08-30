package app

import (
	"context"
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
