package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/sessions"
)

func TestSessionMutationReconcilerRejectsDamagedReceiptStorage(t *testing.T) {
	for _, kind := range []string{"canonical malformed", "legacy missing", "legacy malformed", "legacy mismatch"} {
		t.Run(kind, func(t *testing.T) { runDamagedFinalizedReceipt(t, kind) })
	}
}

func runDamagedFinalizedReceipt(t *testing.T, kind string) {
	t.Helper()
	h := newFinalizationHarness(t)
	journalDir := filepath.Join(h.sd.SessionsDir(), "mutations")
	record := finalizationRecord(t, h, "idem-finalization-open")
	path := filepath.Join(h.sd.ReceiptsDir(), string(record.ReceiptID)+".json")
	if kind != "canonical malformed" {
		rewriteReservationAsLegacy(t, journalDir, record.IdempotencyKey)
	}
	switch kind {
	case "legacy missing":
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	case "legacy mismatch":
		conflict := *record.Receipt
		conflict.IdempotencyKey = "different-reservation"
		writeBatchFixture(t, path, receipt.ConvertToDTO(conflict))
	default:
		if err := os.WriteFile(path, []byte("{corrupt"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	auditPath := filepath.Join(h.sd.AuditDir(), audit.AuditFileName)
	before, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := restartedFinalizationService(h).ReconcileMutationFinalizations(context.Background(), time.Now()); n != 0 || err == nil {
		t.Fatalf("damaged receipt reconciliation = %d, error %v", n, err)
	}
	after, err := os.ReadFile(auditPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("damaged receipt modified audit evidence: %v", err)
	}
	if got := finalizationRecord(t, h, record.IdempotencyKey); got.State != sessions.MutationReservationFinalized {
		t.Fatalf("damaged receipt changed reservation state: %s", got.State)
	}
}

func TestSessionMutationReconcilerStorageFailuresRemainRecoverable(t *testing.T) {
	for _, boundary := range []string{"intent", "finalize", "cancel before finalize"} {
		t.Run(boundary, func(t *testing.T) { runReconciliationStorageFailure(t, boundary) })
	}
}

func runReconciliationStorageFailure(t *testing.T, boundary string) {
	t.Helper()
	h := newFinalizationHarness(t)
	build := buildJournalFinalizeCut
	wantState := sessions.MutationReservationFinalizing
	if boundary == "intent" {
		build, wantState = buildIntentCut, sessions.MutationReservationPending
	}
	if boundary == "cancel before finalize" {
		build = buildAuditCut
	}
	receipts, audits, journal := build(h)
	params := h.writeParams("startup-storage-failure")
	if _, _, err := h.service(receipts, audits, journal).WriteSession(context.Background(), params); err == nil {
		t.Fatal("fixture failed to interrupt finalization")
	}
	injected := errors.New("synthetic startup journal refusal")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	journal = sessions.NewMutationJournal(filepath.Join(h.sd.SessionsDir(), "mutations"), sessions.WithMutationJournalHook(func(action string) error {
		if action == boundary {
			return injected
		}
		return nil
	}))
	audits = audit.NewStore(h.sd.AuditDir())
	if boundary == "cancel before finalize" {
		audits = audit.NewStore(h.sd.AuditDir(), audit.WithPostAppendHook(cancel))
		injected = context.Canceled
	}
	svc := h.service(receipt.NewStore(h.sd.ReceiptsDir()), audits, journal)
	if n, err := svc.ReconcileMutationFinalizations(ctx, time.Now()); n != 0 || !errors.Is(err, injected) {
		t.Fatalf("failed reconciliation = %d, error %v", n, err)
	}
	if got := finalizationRecord(t, h, params.IdempotencyKey); got.State != wantState {
		t.Fatalf("failure changed recoverable state: got %s, want %s", got.State, wantState)
	}
	if atomic.LoadInt32(&h.transport.writeCalls) != 1 {
		t.Fatal("failed reconciliation repeated guest effects")
	}
	if boundary == "intent" {
		assertInterruptedPendingBecomesUnknownWithoutEffect(t, h, params)
	} else {
		assertRetryFinalizesWithoutEffect(t, h, params, finalizationRecord(t, h, params.IdempotencyKey).ReceiptID)
	}
}

func finalizationRecord(t *testing.T, h *finalizationHarness, key string) sessions.MutationReservation {
	t.Helper()
	records, err := sessions.NewMutationJournal(filepath.Join(h.sd.SessionsDir(), "mutations")).ListContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.IdempotencyKey == key {
			return record
		}
	}
	t.Fatalf("reservation %q missing", key)
	return sessions.MutationReservation{}
}
