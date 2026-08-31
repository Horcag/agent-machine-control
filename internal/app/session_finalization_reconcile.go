package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/sessions"
)

// ReconcileMutationFinalizations resumes durable session mutation finalization without replaying guest effects.
func (s *SessionService) ReconcileMutationFinalizations(ctx context.Context, now time.Time) (int, error) {
	if s == nil || s.mutationJournal == nil {
		return 0, errors.New("app: session mutation journal is unavailable")
	}
	records, err := s.mutationJournal.ListContext(ctx)
	if err != nil {
		return 0, err
	}
	reconciled := 0
	for i := range records {
		changed, err := s.reconcileMutationReservation(ctx, &records[i], now)
		if err != nil {
			return reconciled, err
		}
		if changed {
			reconciled++
		}
	}
	return reconciled, nil
}

func (s *SessionService) reconcileMutationReservation(ctx context.Context, record *sessions.MutationReservation, now time.Time) (bool, error) {
	if record == nil {
		return false, errors.New("app: nil mutation reservation")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	changed, err := s.ensureRecoveryIntent(ctx, record, now)
	if err != nil {
		return false, err
	}
	if s.receiptStore == nil || s.auditStore == nil {
		return changed, errors.New("app: terminal mutation stores are unavailable")
	}
	if record.Receipt == nil {
		return changed, s.verifyLegacyFinalization(ctx, *record)
	}
	if err := s.receiptStore.EnsureContext(ctx, *record.Receipt); err != nil {
		return changed, fmt.Errorf("app: reconcile mutation receipt: %w", err)
	}
	if err := s.auditStore.EnsureTerminalOutcomeContext(ctx, *record.Receipt); err != nil {
		return changed, fmt.Errorf("app: reconcile mutation audit: %w", err)
	}
	if record.State != sessions.MutationReservationFinalizing {
		return changed, nil
	}
	return true, s.mutationJournal.MarkFinalizedRecordContext(ctx, *record, now)
}

func (s *SessionService) ensureRecoveryIntent(ctx context.Context, record *sessions.MutationReservation, now time.Time) (bool, error) {
	if record.State != sessions.MutationReservationPending {
		return false, nil
	}
	receipt, err := interruptedMutationReceipt(*record, now)
	if err != nil {
		return false, err
	}
	if err := s.mutationJournal.RecordFinalizationIntentForRecordContext(ctx, *record, receipt, sessions.MutationResult{}, now); err != nil {
		return false, err
	}
	startedAt := now.UTC()
	record.State = sessions.MutationReservationFinalizing
	record.FinalizationStartedAt = &startedAt
	record.ReceiptID = receipt.ReceiptID
	record.Receipt = &receipt
	record.Result = sessions.MutationResult{}
	return true, nil
}

func (s *SessionService) verifyLegacyFinalization(ctx context.Context, record sessions.MutationReservation) error {
	if record.State != sessions.MutationReservationFinalized {
		return errors.New("app: mutation finalization intent is missing its canonical receipt")
	}
	legacyReceipt, err := s.receiptStore.GetContext(ctx, string(record.ReceiptID))
	if err != nil {
		return err
	}
	if !receiptMatchesMutationRecord(*legacyReceipt, record) {
		return sessions.ErrMutationReservationCollision
	}
	return s.auditStore.VerifyTerminalOutcomeContext(ctx, *legacyReceipt)
}

func receiptMatchesMutationRecord(receipt domain.Receipt, record sessions.MutationReservation) bool {
	return receipt.ReceiptID == record.ReceiptID && receipt.OperationKind == record.OperationKind &&
		receipt.Fingerprint == record.Fingerprint && receipt.IdempotencyFingerprint == record.IdempotencyFingerprint &&
		receipt.IdempotencyKey == record.IdempotencyKey && receipt.Actor == record.Actor && receipt.Target == record.Target &&
		receipt.Class == record.Classification
}

func interruptedMutationReceipt(record sessions.MutationReservation, now time.Time) (domain.Receipt, error) {
	receiptID, err := domain.GenerateReceiptID()
	if err != nil {
		return domain.Receipt{}, err
	}
	completedAt := now.UTC()
	if !completedAt.After(record.CreatedAt) {
		completedAt = record.CreatedAt.Add(time.Millisecond)
	}
	return domain.Receipt{
		ReceiptID:              receiptID,
		OperationKind:          record.OperationKind,
		Fingerprint:            record.Fingerprint,
		IdempotencyFingerprint: record.IdempotencyFingerprint,
		IdempotencyKey:         record.IdempotencyKey,
		Actor:                  record.Actor,
		Target:                 record.Target,
		Class:                  record.Classification,
		EffectiveBackend:       "amcd",
		StartedAt:              record.CreatedAt,
		CompletedAt:            completedAt,
		Outcome:                domain.ExecutionOutcome{Status: domain.OutcomeFailed, ExitCode: 1},
		ObservationType:        domain.ObservationInferred,
		RedactionStatus:        domain.RedactionApplied,
	}, nil
}
