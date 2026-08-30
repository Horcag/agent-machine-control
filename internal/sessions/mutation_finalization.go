package sessions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

// RecordFinalizationIntent durably stores immutable post-effect truth before receipt or audit writes.
func (j *MutationJournal) RecordFinalizationIntent(op domain.Operation, receipt domain.Receipt, result MutationResult, now time.Time) error {
	return j.RecordFinalizationIntentContext(context.Background(), op, receipt, result, now)
}

// RecordFinalizationIntentContext is the context-aware finalization-intent variant.
func (j *MutationJournal) RecordFinalizationIntentContext(ctx context.Context, op domain.Operation, receipt domain.Receipt, result MutationResult, now time.Time) error {
	record, err := j.LookupContext(ctx, op)
	if err != nil {
		return err
	}
	if record == nil {
		return errors.New("sessions: mutation reservation is missing")
	}
	return j.RecordFinalizationIntentForRecordContext(ctx, *record, receipt, result, now)
}

// RecordFinalizationIntentForRecordContext records intent from an enumerated reservation during recovery.
func (j *MutationJournal) RecordFinalizationIntentForRecordContext(ctx context.Context, expected MutationReservation, receipt domain.Receipt, result MutationResult, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := j.callHook("intent"); err != nil {
		return err
	}
	current, err := j.readContext(ctx, j.pathFor(expected.IdempotencyKey))
	if err != nil {
		return err
	}
	if !sameReservationIdentity(*current, expected) || !receiptMatchesReservation(receipt, *current) {
		return ErrMutationReservationCollision
	}
	if err := receipt.Validate(); err != nil {
		return err
	}
	unknownRecovery := receipt.Outcome.Status == domain.OutcomeFailed && receipt.ObservationType == domain.ObservationInferred
	if result.EffectApplied == nil && !unknownRecovery {
		return errors.New("sessions: mutation finalization requires explicit effect truth")
	}
	if current.State != MutationReservationPending {
		if current.Receipt == nil || !reflect.DeepEqual(*current.Receipt, receipt) || !reflect.DeepEqual(current.Result, result) {
			return ErrMutationReservationCollision
		}
		return nil
	}
	startedAt := now.UTC()
	current.SchemaVersion = mutationReservationSchemaVersion
	current.State = MutationReservationFinalizing
	current.FinalizationStartedAt = &startedAt
	current.ReceiptID = receipt.ReceiptID
	current.Receipt = &receipt
	current.Result = result
	return j.replaceContext(ctx, j.pathFor(current.IdempotencyKey), *current)
}

// MarkFinalized marks a finalization intent complete after exact receipt and audit records exist.
func (j *MutationJournal) MarkFinalized(op domain.Operation, now time.Time) error {
	return j.MarkFinalizedContext(context.Background(), op, now)
}

// MarkFinalizedContext is the context-aware finalized transition.
func (j *MutationJournal) MarkFinalizedContext(ctx context.Context, op domain.Operation, now time.Time) error {
	record, err := j.LookupContext(ctx, op)
	if err != nil {
		return err
	}
	if record == nil {
		return errors.New("sessions: mutation reservation is missing")
	}
	return j.MarkFinalizedRecordContext(ctx, *record, now)
}

// MarkFinalizedRecordContext finalizes an enumerated recovery record without reconstructing the operation.
func (j *MutationJournal) MarkFinalizedRecordContext(ctx context.Context, expected MutationReservation, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := j.callHook("finalize"); err != nil {
		return err
	}
	current, err := j.readContext(ctx, j.pathFor(expected.IdempotencyKey))
	if err != nil {
		return err
	}
	if !sameReservationIdentity(*current, expected) {
		return ErrMutationReservationCollision
	}
	if current.State == MutationReservationFinalized {
		return nil
	}
	if current.State != MutationReservationFinalizing || current.Receipt == nil {
		return ErrMutationFinalizationPending
	}
	finalizedAt := now.UTC()
	current.SchemaVersion = mutationReservationSchemaVersion
	current.State = MutationReservationFinalized
	current.FinalizedAt = &finalizedAt
	return j.replaceContext(ctx, j.pathFor(current.IdempotencyKey), *current)
}

// ListContext returns all durable mutation reservations in deterministic order.
func (j *MutationJournal) ListContext(ctx context.Context) ([]MutationReservation, error) {
	if j == nil || j.dir == "" {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := j.callHook("list"); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(j.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	records := make([]MutationReservation, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.Contains(entry.Name(), ".tmp.") {
			continue
		}
		record, err := j.readContext(ctx, filepath.Join(j.dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		if entry.Name() != reservationName(record.IdempotencyKey) {
			return nil, ErrMutationReservationCollision
		}
		records = append(records, *record)
	}
	sort.Slice(records, func(i, k int) bool { return records[i].IdempotencyKey < records[k].IdempotencyKey })
	return records, nil
}

func sameReservationIdentity(a, b MutationReservation) bool {
	return a.OperationKind == b.OperationKind && a.Actor == b.Actor && a.Target == b.Target &&
		a.Classification == b.Classification && a.IdempotencyKey == b.IdempotencyKey &&
		a.IdempotencyFingerprint == b.IdempotencyFingerprint && a.Fingerprint == b.Fingerprint &&
		a.CreatedAt.Equal(b.CreatedAt)
}

func receiptMatchesReservation(receipt domain.Receipt, record MutationReservation) bool {
	return receipt.OperationKind == record.OperationKind && receipt.Actor == record.Actor &&
		receipt.Target == record.Target && receipt.Class == record.Classification &&
		receipt.IdempotencyKey == record.IdempotencyKey && receipt.IdempotencyFingerprint == record.IdempotencyFingerprint &&
		receipt.Fingerprint == record.Fingerprint
}
