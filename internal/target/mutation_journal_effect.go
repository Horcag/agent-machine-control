package target

import (
	"context"
	"errors"
	"os"
	"reflect"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

// RecordEffectContext durably stores semantic Store effect truth before public evidence finalization.
func (j *MutationJournal) RecordEffectContext(ctx context.Context, op domain.Operation, receipt domain.Receipt, committed, durable bool) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	path := j.pathFor(op)
	record, err := j.readContext(ctx, path)
	if err != nil {
		return err
	}
	if !recordMatchesOperation(*record, op) {
		return ErrMutationCollision
	}
	if record.EffectApplied {
		if record.Committed != committed || record.Durable != durable || record.Receipt == nil || !reflect.DeepEqual(*record.Receipt, receipt) {
			return ErrMutationCollision
		}
		return nil
	}
	if record.State != MutationPending || !committed {
		return ErrMutationFinalization
	}
	if err := receipt.Validate(); err != nil || receipt.Fingerprint != record.Fingerprint || receipt.IdempotencyFingerprint != record.IdempotencyFingerprint {
		return ErrMutationCollision
	}
	if err := j.runHook("effect"); err != nil {
		return err
	}
	record.State = MutationFinalizing
	record.EffectApplied = true
	record.Committed = true
	record.Durable = durable
	copyReceipt := receipt
	record.Receipt = &copyReceipt
	return j.replaceContext(ctx, path, *record)
}

// RecordEffectForRecordContext persists effect truth during startup without reconstructing raw request fields.
func (j *MutationJournal) RecordEffectForRecordContext(ctx context.Context, expected MutationRecord, receipt domain.Receipt, durable bool) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	path := j.pathForIdentity(expected.Actor, expected.IdempotencyKey)
	record, err := j.readContext(ctx, path)
	if err != nil {
		return err
	}
	if !sameMutationIdentity(*record, expected) {
		return ErrMutationCollision
	}
	if record.EffectApplied {
		if !record.Committed || record.Receipt == nil || !reflect.DeepEqual(*record.Receipt, receipt) || (record.Durable && !durable) {
			return ErrMutationCollision
		}
		if !record.Durable && durable {
			record.Durable = true
			return j.replaceContext(ctx, path, *record)
		}
		return nil
	}
	if record.State != MutationPending || receipt.Fingerprint != record.Fingerprint ||
		receipt.IdempotencyFingerprint != record.IdempotencyFingerprint || receipt.Validate() != nil {
		return ErrMutationFinalization
	}
	record.State = MutationFinalizing
	record.EffectApplied = true
	record.Committed = true
	record.Durable = durable
	copyReceipt := receipt
	record.Receipt = &copyReceipt
	return j.replaceContext(ctx, path, *record)
}

// CancelRecordContext removes an exact startup reservation proven to have no Store effect.
func (j *MutationJournal) CancelRecordContext(ctx context.Context, expected MutationRecord) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	path := j.pathForIdentity(expected.Actor, expected.IdempotencyKey)
	record, err := j.readContext(ctx, path)
	if errors.Is(err, ErrMutationNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !sameMutationIdentity(*record, expected) || record.EffectApplied || record.State != MutationPending {
		return ErrMutationFinalization
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return ErrMutationFinalization
	}
	return statedir.SyncDir(j.dir)
}

// MarkFinalizedContext records that receipt and terminal audit evidence are durable.
func (j *MutationJournal) MarkFinalizedContext(ctx context.Context, op domain.Operation, now time.Time) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	path := j.pathFor(op)
	record, err := j.readContext(ctx, path)
	if err != nil {
		return err
	}
	if !recordMatchesOperation(*record, op) {
		return ErrMutationCollision
	}
	if record.State == MutationFinalized {
		return nil
	}
	if record.State != MutationFinalizing || !record.EffectApplied || record.Receipt == nil {
		return ErrMutationFinalization
	}
	if err := j.runHook("finalize"); err != nil {
		return err
	}
	finalizedAt := now.UTC()
	record.State = MutationFinalized
	record.FinalizedAt = &finalizedAt
	return j.replaceContext(ctx, path, *record)
}

// MarkFinalizedForRecordContext completes startup reconciliation for an exact reservation.
func (j *MutationJournal) MarkFinalizedForRecordContext(ctx context.Context, expected MutationRecord, now time.Time) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	path := j.pathForIdentity(expected.Actor, expected.IdempotencyKey)
	record, err := j.readContext(ctx, path)
	if err != nil {
		return err
	}
	if record.Fingerprint != expected.Fingerprint || record.IdempotencyFingerprint != expected.IdempotencyFingerprint ||
		record.Kind != expected.Kind || record.Target != expected.Target {
		return ErrMutationCollision
	}
	if record.State == MutationFinalized {
		return nil
	}
	if record.State != MutationFinalizing || !record.EffectApplied || record.Receipt == nil {
		return ErrMutationFinalization
	}
	finalizedAt := now.UTC()
	record.State = MutationFinalized
	record.FinalizedAt = &finalizedAt
	return j.replaceContext(ctx, path, *record)
}

// CancelContext removes only a reservation proven to have no Store effect.
func (j *MutationJournal) CancelContext(ctx context.Context, op domain.Operation) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	path := j.pathFor(op)
	record, err := j.readContext(ctx, path)
	if errors.Is(err, ErrMutationNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !recordMatchesOperation(*record, op) || record.EffectApplied || record.State != MutationPending {
		return ErrMutationFinalization
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return ErrMutationFinalization
	}
	return statedir.SyncDir(j.dir)
}
