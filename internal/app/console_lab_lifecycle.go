package app

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func (s *ConsoleService) LabGrantStatus(ctx context.Context, actor domain.ActorContext, id string) (ConsoleLabGrantStatus, error) {
	if actor.Validate() != nil || actor.IsDelegated() {
		return ConsoleLabGrantStatus{}, ErrInvalidConsoleLabGrant
	}
	if err := s.validateDependencies(); err != nil {
		return ConsoleLabGrantStatus{}, err
	}
	grant, err := s.loadLabGrant(ctx, id)
	if err != nil {
		return ConsoleLabGrantStatus{}, ErrInvalidConsoleLabGrant
	}
	if actor.EffectiveActor != grant.Beneficiary && !actor.HasScope(domain.ScopeOperationAdmin) {
		return ConsoleLabGrantStatus{}, ErrInvalidConsoleLabGrant
	}
	status := ConsoleLabGrantStatus{Grant: grant, State: "active"}
	revoked, err := s.labRevoked(ctx, id)
	if err != nil {
		return ConsoleLabGrantStatus{}, ErrInvalidConsoleLabGrant
	}
	switch {
	case revoked:
		status.State = "revoked"
	case !s.recovery.now().Before(grant.ExpiresAt):
		status.State = "expired"
	case s.requireActiveLabGrant(ctx, grant) != nil:
		status.State = "stale"
	}
	return status, nil
}

// RevokeLabGrant serializes with guest actions; once it succeeds no new action can dispatch.
func (s *ConsoleService) RevokeLabGrant(ctx context.Context, actor domain.ActorContext, req ConsoleLabGrantRevokeRequest) (result domain.Receipt, resultErr error) {
	if err := labOperator(actor); err != nil {
		return result, err
	}
	deadline, err := time.Parse(time.RFC3339Nano, req.Deadline)
	if err != nil || domain.ValidateReason(req.Reason) != nil || domain.ValidateIdempotencyKey(req.IdempotencyKey) != nil {
		return result, ErrInvalidConsoleLabGrant
	}
	release, err := s.labLock(ctx, req.GrantID)
	if err != nil {
		return result, err
	}
	defer func() { resultErr = errors.Join(resultErr, release()) }()
	grant, err := s.loadLabGrant(ctx, req.GrantID)
	if err != nil {
		return result, ErrInvalidConsoleLabGrant
	}
	now := s.recovery.now()
	if !deadline.After(now) || deadline.After(now.Add(5*time.Minute)) {
		return result, ErrInvalidConsoleLabGrant
	}
	value := consoleLabRevocation{GrantID: req.GrantID, Actor: actor.EffectiveActor, Reason: req.Reason, IdempotencyKey: req.IdempotencyKey, Deadline: deadline, RevokedAt: now}
	var existing consoleLabRevocation
	loadErr := s.readLabDocument(ctx, req.GrantID, ".revoked", &existing)
	if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
		return result, loadErr
	}
	if loadErr == nil {
		value.RevokedAt = existing.RevokedAt
		if value != existing {
			return result, receipt.ErrIdempotencyCollision
		}
	}
	op := labGrantOperation("console.lab.grant.revoke", actor, grant, req.Reason, req.IdempotencyKey, deadline)
	op.Parameters = map[string]any{"grant_id": req.GrantID}
	return s.persistLabAuthority(ctx, op, value.RevokedAt, req.GrantID, func() error {
		if loadErr == nil {
			return nil
		}
		return s.writeLabDocument(ctx, req.GrantID, ".revoked", value)
	})
}
