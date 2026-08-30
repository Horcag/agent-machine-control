package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/sessions"
)

func (s *SessionService) checkCachedReceipt(ctx context.Context, op domain.Operation) (*domain.Receipt, error) {
	if s.receiptStore == nil {
		return nil, nil
	}
	cached, err := s.receiptStore.LookupIdempotencyContext(ctx, op)
	if err == nil || !errors.Is(err, receipt.ErrIdempotencyCollision) || op.Classification != domain.ClassReversibleMutation {
		return cached, err
	}
	destructiveOp := op.Clone()
	destructiveOp.Classification = domain.ClassDestructivePrivileged
	return s.receiptStore.LookupIdempotencyContext(ctx, destructiveOp)
}

func (s *SessionService) lookupMutationReservation(ctx context.Context, op domain.Operation) (*sessions.MutationReservation, domain.Operation, error) {
	if s.mutationJournal == nil {
		return nil, op, nil
	}
	record, err := s.mutationJournal.LookupContext(ctx, op)
	if err == nil || !errors.Is(err, sessions.ErrMutationReservationCollision) || op.Classification != domain.ClassReversibleMutation {
		if errors.Is(err, sessions.ErrMutationReservationCollision) {
			err = fmt.Errorf("%w: durable mutation reservation", receipt.ErrIdempotencyCollision)
		}
		return record, op, err
	}
	destructiveOp := op.Clone()
	destructiveOp.Classification = domain.ClassDestructivePrivileged
	record, err = s.mutationJournal.LookupContext(ctx, destructiveOp)
	if errors.Is(err, sessions.ErrMutationReservationCollision) {
		err = fmt.Errorf("%w: durable mutation reservation", receipt.ErrIdempotencyCollision)
	}
	return record, destructiveOp, err
}

func (s *SessionService) lookupSessionRetry(ctx context.Context, op domain.Operation) (int, *domain.SessionObservation, *domain.Receipt, bool, error) {
	if s.mutationJournal != nil {
		reservation, reservedOp, err := s.lookupMutationReservation(ctx, op)
		if err != nil {
			return 0, nil, nil, true, err
		}
		if reservation != nil {
			n, obs, rcpt, retryErr := s.handleReservedRetry(ctx, reservedOp, reservation)
			return n, obs, rcpt, true, retryErr
		}
	}
	cached, err := s.checkCachedReceipt(ctx, op)
	if err != nil {
		return 0, nil, cached, true, err
	}
	if cached == nil {
		return 0, nil, nil, false, nil
	}
	rcpt, retryErr := s.handleExactRetry(ctx, cached)
	return 0, nil, rcpt, true, retryErr
}

func (s *SessionService) handleExactRetry(ctx context.Context, cached *domain.Receipt) (*domain.Receipt, error) {
	if cached.Outcome.Status == domain.OutcomeDenied {
		if s.auditStore == nil {
			return cached, errors.New("app: cached denial audit store is unavailable")
		}
		if err := s.auditStore.EnsureTerminalOutcomeContext(ctx, *cached); err != nil {
			return cached, fmt.Errorf("app: cached denial audit reconciliation failed: %w", err)
		}
		if err := s.auditStore.VerifyTerminalOutcomeContext(ctx, *cached); err != nil {
			return cached, fmt.Errorf("app: cached denial audit verification failed: %w", err)
		}
		return cached, &PolicyDeniedError{Reason: policy.DenialReason(cached.Outcome.ErrorCategory), Message: cached.Outcome.ErrorMessage}
	}
	return cached, errors.New("app: terminal session receipt is missing its durable mutation reservation")
}

func (s *SessionService) handleReservedRetry(ctx context.Context, op domain.Operation, reservation *sessions.MutationReservation) (int, *domain.SessionObservation, *domain.Receipt, error) {
	reservation, err := s.ensureFinalizedReservation(ctx, op, reservation)
	if err != nil {
		return 0, nil, nil, err
	}
	rcpt, err := s.loadVerifiedReservationReceipt(ctx, op, reservation)
	if err != nil {
		return 0, nil, nil, err
	}
	return replayMutationResult(reservation, rcpt)
}

func (s *SessionService) ensureFinalizedReservation(ctx context.Context, op domain.Operation, reservation *sessions.MutationReservation) (*sessions.MutationReservation, error) {
	if reservation.State == sessions.MutationReservationFinalized {
		return reservation, nil
	}
	if _, err := s.reconcileMutationReservation(ctx, reservation, s.now()); err != nil {
		return nil, err
	}
	reloaded, _, err := s.lookupMutationReservation(ctx, op)
	if err != nil {
		return nil, err
	}
	if reloaded == nil || reloaded.State != sessions.MutationReservationFinalized {
		return nil, sessions.ErrMutationFinalizationPending
	}
	return reloaded, nil
}

func (s *SessionService) loadVerifiedReservationReceipt(ctx context.Context, op domain.Operation, reservation *sessions.MutationReservation) (*domain.Receipt, error) {
	if s.receiptStore == nil {
		return nil, errors.New("app: receipt store is unavailable for finalized mutation")
	}
	rcpt, err := s.receiptStore.GetContext(ctx, string(reservation.ReceiptID))
	if err != nil {
		return nil, fmt.Errorf("app: finalized mutation receipt is unavailable: %w", err)
	}
	idFp, err := domain.ComputeIdempotencyFingerprint(op)
	if err != nil {
		return nil, err
	}
	if err := verifyReservedReceipt(op, reservation, rcpt, idFp); err != nil {
		return nil, err
	}
	if reservation.Receipt != nil && !reflect.DeepEqual(*reservation.Receipt, *rcpt) {
		return nil, sessions.ErrMutationReservationCollision
	}
	if s.auditStore == nil {
		return nil, errors.New("app: audit store is unavailable for finalized mutation")
	}
	if err := s.auditStore.VerifyTerminalOutcomeContext(ctx, *rcpt); err != nil {
		return nil, fmt.Errorf("app: finalized mutation audit evidence is invalid: %w", err)
	}
	return rcpt, nil
}

func replayMutationResult(reservation *sessions.MutationReservation, rcpt *domain.Receipt) (int, *domain.SessionObservation, *domain.Receipt, error) {
	result := reservation.Result
	_, effectKnown := result.EffectTruth(reservation.OperationKind)
	if !effectKnown {
		return result.BytesWritten, result.Observation, rcpt, sessions.ErrMutationEffectUnknown
	}
	switch rcpt.Outcome.Status {
	case domain.OutcomeDenied:
		return result.BytesWritten, result.Observation, rcpt, &PolicyDeniedError{Reason: policy.DenialReason(rcpt.Outcome.ErrorCategory), Message: rcpt.Outcome.ErrorMessage}
	case domain.OutcomeAborted:
		if rcpt.Outcome.ErrorCategory == "caller_canceled" {
			return result.BytesWritten, result.Observation, rcpt, context.Canceled
		}
		return result.BytesWritten, result.Observation, rcpt, context.DeadlineExceeded
	case domain.OutcomeFailed:
		return result.BytesWritten, result.Observation, rcpt, errors.New("session mutation failed")
	default:
		return result.BytesWritten, result.Observation, rcpt, nil
	}
}

func verifyReservedReceipt(op domain.Operation, reservation *sessions.MutationReservation, rcpt *domain.Receipt, idFp domain.Fingerprint) error {
	if rcpt.OperationKind != reservation.OperationKind || rcpt.Fingerprint != reservation.Fingerprint {
		return sessions.ErrMutationReservationCollision
	}
	if rcpt.IdempotencyFingerprint != reservation.IdempotencyFingerprint || rcpt.IdempotencyFingerprint != idFp || rcpt.IdempotencyKey != reservation.IdempotencyKey {
		return sessions.ErrMutationReservationCollision
	}
	if rcpt.Actor != reservation.Actor || rcpt.Actor != op.Actor.EffectiveActor || rcpt.Target != reservation.Target || rcpt.Target != op.Target {
		return sessions.ErrMutationReservationCollision
	}
	if rcpt.Class != reservation.Classification {
		return sessions.ErrMutationReservationCollision
	}
	return nil
}
