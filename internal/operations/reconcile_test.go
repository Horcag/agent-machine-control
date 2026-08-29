package operations_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/events"
	"github.com/Horcag/agent-machine-control/internal/operations"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func TestOperations_ReconcileCrashedOperations(t *testing.T) {
	dir := t.TempDir()
	rcptDir := t.TempDir()
	auditDir := t.TempDir()

	rcptStore := receipt.NewStore(rcptDir)
	auditStore := audit.NewStore(auditDir)
	eventHub := events.NewHub(dir)

	digest := sha256.Sum256([]byte("fingerprint-crashed"))
	fp := domain.Fingerprint("sha256:" + hex.EncodeToString(digest[:]))

	target := domain.MachineRef("c4a523d4-6b99-4d62-a5e2-4752c0f20001")
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	crashedID := "op-00000000000000000000000000000001"
	completedID := "op-00000000000000000000000000000002"

	// Record 1: running (crashed)
	recRunning := domain.OperationRecord{
		SchemaVersion:  "1",
		ID:             crashedID,
		Actor:          "agent:mcp-local",
		Target:         target,
		Kind:           "machine.start",
		RequestedClass: domain.ClassReversibleMutation,
		EffectiveClass: domain.ClassReversibleMutation,
		Fingerprint:    fp,
		IdempotencyKey: "idem-crashed-1",
		Deadline:       now.Add(time.Hour),
		State:          domain.OpStateRunning,
		CreatedAt:      now.Add(-10 * time.Minute),
	}
	if err := operations.SaveRecord(dir, recRunning); err != nil {
		t.Fatalf("save recRunning failed: %v", err)
	}

	// Record 2: completed (should not be altered)
	recCompleted := domain.OperationRecord{
		SchemaVersion:  "1",
		ID:             completedID,
		Actor:          "agent:mcp-local",
		Target:         target,
		Kind:           "machine.start",
		RequestedClass: domain.ClassReversibleMutation,
		EffectiveClass: domain.ClassReversibleMutation,
		Fingerprint:    fp,
		IdempotencyKey: "idem-completed-1",
		Deadline:       now.Add(time.Hour),
		State:          domain.OpStateCompleted,
		CreatedAt:      now.Add(-20 * time.Minute),
		CompletedAt:    now.Add(-15 * time.Minute),
		ReceiptID:      "rcpt-00000000000000000000000000000001",
	}
	if err := operations.SaveRecord(dir, recCompleted); err != nil {
		t.Fatalf("save recCompleted failed: %v", err)
	}

	ctx := context.Background()
	reconciled, err := operations.ReconcileCrashedOperations(ctx, dir, rcptStore, auditStore, eventHub, now)
	if err != nil {
		t.Fatalf("ReconcileCrashedOperations failed: %v", err)
	}

	if len(reconciled) != 1 || reconciled[0] != crashedID {
		t.Fatalf("expected %s to be reconciled, got: %v", crashedID, reconciled)
	}

	// Verify updated record
	updated, err := operations.ReadRecord(dir, crashedID)
	if err != nil {
		t.Fatalf("ReadRecord failed: %v", err)
	}
	if updated.State != domain.OpStateCancelled {
		t.Errorf("expected state cancelled, got %s", updated.State)
	}
	if updated.ErrorCategory != "daemon_crash_recovered" {
		t.Errorf("expected error category daemon_crash_recovered, got %s", updated.ErrorCategory)
	}
	if updated.ReceiptID == "" {
		t.Errorf("expected synthetic receipt to be generated")
	}

	// Verify synthetic receipt was saved in receiptStore
	rcpt, err := rcptStore.Get(string(updated.ReceiptID))
	if err != nil {
		t.Fatalf("failed to fetch synthetic receipt: %v", err)
	}
	if rcpt.Outcome.Status != domain.OutcomeAborted {
		t.Errorf("expected outcome status aborted, got %s", rcpt.Outcome.Status)
	}
}
