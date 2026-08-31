package target

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestMutationJournalOperationLifecycle(t *testing.T) {
	journal, op, record, now, _ := newMutationJournalRecord(t, "journal-operation-lifecycle")
	if err := journal.CheckWritableContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if loaded, err := journal.LookupContext(context.Background(), op); err != nil || loaded.Fingerprint != record.Fingerprint {
		t.Fatalf("LookupContext = %+v, %v", loaded, err)
	}
	receipt := mutationEffectReceipt(*record, now.Add(time.Second))
	if err := journal.RecordEffectContext(context.Background(), op, receipt, true, true); err != nil {
		t.Fatal(err)
	}
	if err := journal.MarkFinalizedContext(context.Background(), op, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	finalized, err := journal.LookupKeyContext(context.Background(), op.Actor.EffectiveActor, op.IdempotencyKey)
	if err != nil || finalized.State != MutationFinalized || finalized.Receipt == nil {
		t.Fatalf("finalized = %+v, %v", finalized, err)
	}
	if err := journal.MarkFinalizedContext(context.Background(), op, now.Add(3*time.Second)); err != nil {
		t.Fatalf("idempotent finalize: %v", err)
	}
}

func TestMutationJournalRecordLifecycleRepairsDurability(t *testing.T) {
	journal, _, record, now, _ := newMutationJournalRecord(t, "journal-record-lifecycle")
	receipt := mutationEffectReceipt(*record, now.Add(time.Second))
	if err := journal.RecordEffectForRecordContext(context.Background(), *record, receipt, false); err != nil {
		t.Fatal(err)
	}
	if err := journal.RecordEffectForRecordContext(context.Background(), *record, receipt, true); err != nil {
		t.Fatal(err)
	}
	if err := journal.MarkFinalizedForRecordContext(context.Background(), *record, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	loaded, err := journal.LookupKeyContext(context.Background(), record.Actor, record.IdempotencyKey)
	if err != nil || loaded.State != MutationFinalized || !loaded.Durable {
		t.Fatalf("durability repair = %+v, %v", loaded, err)
	}
	changed := receipt
	changed.ReceiptID = "rcpt-00000000000000000000000000000002"
	if err := journal.RecordEffectForRecordContext(context.Background(), *record, changed, true); !errors.Is(err, ErrMutationCollision) {
		t.Fatalf("changed receipt error = %v", err)
	}
}

func TestMutationJournalCancellationPaths(t *testing.T) {
	journal, op, _, _, _ := newMutationJournalRecord(t, "journal-cancel-operation")
	if err := journal.CancelContext(context.Background(), op); err != nil {
		t.Fatal(err)
	}
	if err := journal.CancelContext(context.Background(), op); err != nil {
		t.Fatalf("idempotent operation cancel: %v", err)
	}

	journal, _, record, _, _ := newMutationJournalRecord(t, "journal-cancel-record")
	if err := journal.CancelRecordContext(context.Background(), *record); err != nil {
		t.Fatal(err)
	}
	if err := journal.CancelRecordContext(context.Background(), *record); err != nil {
		t.Fatalf("idempotent record cancel: %v", err)
	}
}

func TestMutationJournalReserveHookFailsBeforeStateTransition(t *testing.T) {
	synthetic := errors.New("synthetic journal boundary failure")
	journal, op, _, now := newMutationJournalWithHookRecord(t, "reserve", synthetic)
	if _, err := journal.ReserveContext(context.Background(), op, StateDigest(nil), StateDigest(nil), StateDigest(nil), 0, now); !errors.Is(err, synthetic) {
		t.Fatalf("reserve hook error = %v", err)
	}
}

func TestMutationJournalEffectHookFailsBeforeStateTransition(t *testing.T) {
	synthetic := errors.New("synthetic journal boundary failure")
	journal, op, record, now := newMutationJournalWithHookRecord(t, "effect", synthetic)
	receipt := mutationEffectReceipt(*record, now.Add(time.Second))
	if err := journal.RecordEffectContext(context.Background(), op, receipt, true, true); !errors.Is(err, synthetic) {
		t.Fatalf("effect hook error = %v", err)
	}
}

func TestMutationJournalFinalizeHookFailsBeforeStateTransition(t *testing.T) {
	synthetic := errors.New("synthetic journal boundary failure")
	journal, op, record, now := newMutationJournalWithHookRecord(t, "finalize", synthetic)
	receipt := mutationEffectReceipt(*record, now.Add(time.Second))
	if err := journal.RecordEffectContext(context.Background(), op, receipt, true, true); err != nil {
		t.Fatal(err)
	}
	if err := journal.MarkFinalizedContext(context.Background(), op, now.Add(2*time.Second)); !errors.Is(err, synthetic) {
		t.Fatalf("finalize hook error = %v", err)
	}
}

func newMutationJournalWithHookRecord(t *testing.T, failAction string, synthetic error) (*MutationJournal, domain.Operation, *MutationRecord, time.Time) {
	t.Helper()
	now := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	deadline := now.Add(time.Minute)
	op := mutationTestOperation(mutationTestActor(t), "journal-hook-"+failAction, deadline)
	journal, err := NewMutationJournal(t.TempDir(), WithMutationJournalHook(func(action string) error {
		if action == failAction {
			return synthetic
		}
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if failAction == "reserve" {
		return journal, op, &MutationRecord{}, now
	}
	record, err := journal.ReserveContext(context.Background(), op, StateDigest(nil), StateDigest(nil), StateDigest(nil), 0, now)
	if err != nil {
		t.Fatal(err)
	}
	return journal, op, record, now
}

func mutationEffectReceipt(record MutationRecord, completedAt time.Time) domain.Receipt {
	return domain.Receipt{
		ReceiptID: "rcpt-00000000000000000000000000000001", OperationKind: record.Kind,
		Fingerprint: record.Fingerprint, IdempotencyFingerprint: record.IdempotencyFingerprint,
		IdempotencyKey: record.IdempotencyKey, Actor: record.Actor, Target: record.Target,
		Class: domain.ClassDestructivePrivileged, EffectiveBackend: "control-plane",
		StartedAt: record.CreatedAt, CompletedAt: completedAt,
		Outcome:         domain.ExecutionOutcome{Status: domain.OutcomeSuccess, ExitCode: 0},
		ObservationType: domain.ObservationObserved, EvidenceRefs: []string{record.TransitionHash},
		RedactionStatus: domain.RedactionApplied,
	}
}
