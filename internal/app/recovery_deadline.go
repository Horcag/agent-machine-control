package app

import (
	"context"
	"errors"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
)

func (s *RecoveryService) mutationExecutionContext(parent context.Context, op domain.Operation, req MutationRequest) (context.Context, context.CancelFunc) {
	budget := op.Deadline.Sub(s.now())
	if req.Timeout > 0 && req.Timeout < budget {
		budget = req.Timeout
	}
	return context.WithTimeoutCause(parent, budget, context.DeadlineExceeded)
}

func boundedMutationFinalizationContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
}

func (s *RecoveryService) preProviderFailure(
	ctx context.Context,
	op domain.Operation,
	fp domain.Fingerprint,
	decision policy.Decision,
	startedAt time.Time,
	primary error,
	rollbackRef string,
) (domain.Receipt, error) {
	if !errors.Is(primary, context.Canceled) && !errors.Is(primary, context.DeadlineExceeded) {
		return domain.Receipt{}, primary
	}
	if decision.EffectiveClass == "" {
		decision.EffectiveClass = op.Classification
	}
	finalizationCtx, cancel := boundedMutationFinalizationContext(ctx)
	defer cancel()
	receiptRecord, persistErr := s.persistOutcome(finalizationCtx, op, fp, decision, startedAt, s.now(), primary, rollbackRef)
	return s.finalizeMutation(receiptRecord, primary, persistErr, nil)
}

func (s *RecoveryService) runProviderExecution(
	ctx context.Context,
	execFn func(context.Context) error,
) (time.Time, time.Time, error) {
	startedAt := s.now()
	runErr := execFn(ctx)
	if runErr == nil && ctx.Err() != nil {
		runErr = ctx.Err()
	}
	completedAt := s.now()
	if !completedAt.After(startedAt) {
		completedAt = startedAt.Add(time.Millisecond)
	}
	return startedAt, completedAt, runErr
}
