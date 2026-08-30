package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/sessions"
)

func (s *SessionService) checkCachedReceipt(op domain.Operation) (*domain.Receipt, error) {
	if s.receiptStore == nil {
		return nil, nil
	}
	cached, err := s.receiptStore.LookupIdempotency(op)
	if err == nil || !errors.Is(err, receipt.ErrIdempotencyCollision) || op.Classification != domain.ClassReversibleMutation {
		return cached, err
	}
	destructiveOp := op.Clone()
	destructiveOp.Classification = domain.ClassDestructivePrivileged
	return s.receiptStore.LookupIdempotency(destructiveOp)
}

func (s *SessionService) lookupMutationReservation(op domain.Operation) (*sessions.MutationReservation, domain.Operation, error) {
	if s.mutationJournal == nil {
		return nil, op, nil
	}
	record, err := s.mutationJournal.Lookup(op)
	if err == nil || !errors.Is(err, sessions.ErrMutationReservationCollision) || op.Classification != domain.ClassReversibleMutation {
		if errors.Is(err, sessions.ErrMutationReservationCollision) {
			err = fmt.Errorf("%w: durable mutation reservation", receipt.ErrIdempotencyCollision)
		}
		return record, op, err
	}
	destructiveOp := op.Clone()
	destructiveOp.Classification = domain.ClassDestructivePrivileged
	record, err = s.mutationJournal.Lookup(destructiveOp)
	if errors.Is(err, sessions.ErrMutationReservationCollision) {
		err = fmt.Errorf("%w: durable mutation reservation", receipt.ErrIdempotencyCollision)
	}
	return record, destructiveOp, err
}

func (s *SessionService) lookupSessionRetry(op domain.Operation) (int, *domain.SessionObservation, *domain.Receipt, bool, error) {
	if s.mutationJournal != nil {
		reservation, reservedOp, err := s.lookupMutationReservation(op)
		if err != nil {
			return 0, nil, nil, true, err
		}
		if reservation != nil {
			n, obs, rcpt, retryErr := s.handleReservedRetry(reservedOp, reservation)
			return n, obs, rcpt, true, retryErr
		}
	}
	cached, err := s.checkCachedReceipt(op)
	if err != nil {
		return 0, nil, cached, true, err
	}
	if cached == nil {
		return 0, nil, nil, false, nil
	}
	rcpt, retryErr := s.handleExactRetry(cached)
	return 0, nil, rcpt, true, retryErr
}

func (s *SessionService) handleExactRetry(cached *domain.Receipt) (*domain.Receipt, error) {
	if cached.Outcome.Status == domain.OutcomeDenied {
		return cached, &PolicyDeniedError{Reason: policy.DenialReason(cached.Outcome.ErrorCategory), Message: cached.Outcome.ErrorMessage}
	}
	return cached, errors.New("app: terminal session receipt is missing its durable mutation reservation")
}

func (s *SessionService) handleReservedRetry(op domain.Operation, reservation *sessions.MutationReservation) (int, *domain.SessionObservation, *domain.Receipt, error) {
	if reservation.State != sessions.MutationReservationFinalized {
		return 0, nil, nil, sessions.ErrMutationFinalizationPending
	}
	if s.receiptStore == nil {
		return 0, nil, nil, errors.New("app: receipt store is unavailable for finalized mutation")
	}
	rcpt, err := s.receiptStore.Get(string(reservation.ReceiptID))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("app: finalized mutation receipt is unavailable: %w", err)
	}
	idFp, err := domain.ComputeIdempotencyFingerprint(op)
	if err != nil {
		return 0, nil, nil, err
	}
	if err := verifyReservedReceipt(op, reservation, rcpt, idFp); err != nil {
		return 0, nil, nil, err
	}
	if s.auditStore == nil {
		return 0, nil, nil, errors.New("app: audit store is unavailable for finalized mutation")
	}
	if err := s.auditStore.VerifyTerminalOutcome(*rcpt); err != nil {
		return 0, nil, nil, fmt.Errorf("app: finalized mutation audit evidence is invalid: %w", err)
	}

	result := reservation.Result
	_, effectKnown := result.EffectTruth(reservation.OperationKind)
	if rcpt.Outcome.Status == domain.OutcomeSuccess && !effectKnown {
		return result.BytesWritten, result.Observation, rcpt, errors.New("app: legacy session mutation result has ambiguous effect truth")
	}
	switch rcpt.Outcome.Status {
	case domain.OutcomeDenied:
		return result.BytesWritten, result.Observation, rcpt, &PolicyDeniedError{Reason: policy.DenialReason(rcpt.Outcome.ErrorCategory), Message: rcpt.Outcome.ErrorMessage}
	case domain.OutcomeAborted:
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
