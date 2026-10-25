package app

import (
	"context"
	"errors"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/target"
)

// WithSessionLabAuthority installs the shared enrolled-VM authority owner.
func WithSessionLabAuthority(authority *ConsoleService) SessionOption {
	return func(s *SessionService) { s.labAuthority = authority }
}

// WithRecoveryLabAuthority installs the same authority for direct and daemon mutations.
func WithRecoveryLabAuthority(authority *ConsoleService) Option {
	return func(s *RecoveryService) { s.labAuthority = authority }
}

func persistentLabOperation(kind domain.OperationKind) bool {
	switch kind {
	case "session.open", "session.write", "session.control", "session.close", "console.input", "desktop.action", "machine.start", "machine.stop", "checkpoint.create", "checkpoint.restore":
		return true
	default:
		return domain.IsGuestOperation(kind)
	}
}

// Caller approvals are resolved before this method; only absent approval can derive authority.
func (s *ConsoleService) admitPersistentLab(ctx context.Context, op domain.Operation) (*ConsoleLabGrant, func() error, error) {
	noop := func() error { return nil }
	if s == nil || !persistentLabOperation(op.Kind) || op.Actor.IsDelegated() {
		return nil, noop, nil
	}
	status, err := s.activeLabGrant(ctx, op.Actor, string(op.Target), true)
	if errors.Is(err, target.ErrNoDefault) || errors.Is(err, target.ErrDifferentTarget) {
		return nil, noop, nil
	}
	if err != nil {
		return nil, noop, err
	}
	if status.State != "active" || status.Grant.Mode != ConsoleLabModePersistentVM {
		return nil, noop, nil
	}
	grant := status.Grant
	release, err := s.labLock(ctx, grant.GrantID)
	if err != nil {
		return nil, noop, err
	}
	if err := s.requireActiveLabGrant(ctx, grant); err != nil {
		return nil, noop, errors.Join(err, release())
	}
	if op.Target != grant.Target || !op.Deadline.After(s.recovery.now()) {
		return nil, noop, errors.Join(ErrInvalidConsoleLabGrant, release())
	}
	return &grant, release, nil
}

func (s *ConsoleService) derivePersistentLab(ctx context.Context, op domain.Operation, admittedAt time.Time) (*domain.Approval, *ConsoleLabGrant, func() error, error) {
	grant, release, err := s.admitPersistentLab(ctx, op)
	if err != nil || grant == nil {
		return nil, grant, release, err
	}
	approval, err := s.deriveLabApproval(ctx, *grant, op, admittedAt)
	if err != nil {
		return nil, nil, func() error { return nil }, errors.Join(err, release())
	}
	return approval, grant, release, nil
}

func (s *ConsoleService) withPersistentLabEffect(ctx context.Context, grant *ConsoleLabGrant, execute func(context.Context) error) error {
	if grant == nil {
		return execute(ctx)
	}
	return s.target.WithEnrollmentFence(ctx, func() error {
		if err := s.requireActiveLabGrant(ctx, *grant); err != nil {
			return err
		}
		return execute(ctx)
	})
}

func (s *RecoveryService) coordinatePersistentRecovery(ctx context.Context, op domain.Operation, req MutationRequest, execute func(context.Context) error) (MutationRequest, func(context.Context) error, func() error, error) {
	release := func() error { return nil }
	if req.Approval != nil || req.ApprovalID != "" || req.ApprovalError != nil {
		return req, execute, release, nil
	}
	admittedAt := s.now()
	derived, grant, release, err := s.labAuthority.derivePersistentLab(ctx, op, admittedAt)
	if err != nil {
		return req, execute, release, err
	}
	if derived == nil {
		return req, execute, release, nil
	}
	req.Approval, req.ApprovalID = derived, string(derived.ID)
	req.labAdmissionAt = admittedAt
	fenced := func(effectCtx context.Context) error {
		return s.labAuthority.withPersistentLabEffect(effectCtx, grant, execute)
	}
	return req, fenced, release, nil
}

// labAdmissionTime preserves causality only for locally derived authority.
// Fresh forward readings still enforce expiry; external approvals get no floor.
func labAdmissionTime(now, admittedAt time.Time, local bool) time.Time {
	if local && now.Before(admittedAt) {
		return admittedAt
	}
	return now
}
