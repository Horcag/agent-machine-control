package receipt_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func TestStore_Save_SyncDirFailure(t *testing.T) {
	dir := t.TempDir()
	syncErr := errors.New("simulated directory fsync failure")
	store := receipt.NewStore(dir, receipt.WithSyncDir(func(_ string) error {
		return syncErr
	}))

	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	alice, _ := domain.NewActorContext("user:alice", "user:alice", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))
	op := domain.Operation{
		Kind:                "machine.start",
		Target:              "a0b1c2d3-e4f5-6789-abcd-ef0123456789",
		Actor:               alice,
		Reason:              "test sync fail",
		Deadline:            now.Add(5 * time.Minute),
		IdempotencyKey:      "key-sync-fail",
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}
	fp, err := op.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint failed: %v", err)
	}

	r := domain.Receipt{
		ReceiptID:        "rcpt-sync-fail",
		OperationKind:    op.Kind,
		Fingerprint:      fp,
		IdempotencyKey:   op.IdempotencyKey,
		Actor:            alice.EffectiveActor,
		Target:           op.Target,
		Class:            op.Classification,
		EffectiveBackend: "hyperv",
		StartedAt:        now,
		CompletedAt:      now.Add(2 * time.Second),
		Outcome:          domain.ExecutionOutcome{Status: domain.OutcomeSuccess, ExitCode: 0},
		ObservationType:  domain.ObservationObserved,
		RollbackRef:      "chk-1",
		RedactionStatus:  domain.RedactionApplied,
	}

	err = store.Save(r)
	if err == nil {
		t.Fatalf("expected error when directory sync fails during receipt Save")
	}
	if !strings.Contains(err.Error(), "failed to sync directory") {
		t.Errorf("expected directory sync error message, got %v", err)
	}
}
