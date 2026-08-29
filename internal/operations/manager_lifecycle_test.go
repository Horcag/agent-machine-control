package operations_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/events"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/operations"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func TestManager_GlobalIdempotencyConflict(t *testing.T) {
	dir := t.TempDir()
	auditDir := t.TempDir()
	rcptDir := t.TempDir()
	leasesDir := t.TempDir()
	approvalsDir := t.TempDir()

	rcptStore := receipt.NewStore(rcptDir)
	auditStore := audit.NewStore(auditDir)
	leaseMgr := lease.NewManager(leasesDir)
	approvalStore := approval.NewStore(approvalsDir)
	eventHub := events.NewHub(dir)

	blockingBackend := &blockingBackend{
		started: make(chan struct{}),
		unblock: make(chan struct{}),
	}
	recoverySvc := app.NewRecoveryService(blockingBackend, leaseMgr, auditStore, rcptStore, approvalStore)

	mgr := operations.NewManager(dir, recoverySvc, rcptStore, auditStore, eventHub)
	defer func() {
		close(blockingBackend.unblock)
		_ = mgr.Shutdown(context.Background())
	}()

	act1, _ := domain.NewActorContext("user:alice", "user:alice", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))
	act2, _ := domain.NewActorContext("user:bob", "user:bob", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))

	target := domain.MachineRef("c4a523d4-6b99-4d62-a5e2-4752c0f20001")
	idemKey := "idem-cross-actor-1"

	op1 := domain.Operation{
		Kind:                "machine.start",
		Target:              target,
		Actor:               act1,
		Reason:              "start 1",
		Deadline:            time.Now().Add(time.Minute),
		IdempotencyKey:      idemKey,
		RequiredCapability:  string(domain.CapabilityMachineStart),
		RequiredScopes:      []string{"machine:write"},
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	_, _, err := mgr.Submit(context.Background(), op1, 30*time.Second)
	if err != nil {
		t.Fatalf("Submit 1 failed: %v", err)
	}

	// Submit with different actor and same key -> MUST conflict!
	op2 := domain.Operation{
		Kind:                "machine.start",
		Target:              target,
		Actor:               act2,
		Reason:              "start 2",
		Deadline:            time.Now().Add(time.Minute),
		IdempotencyKey:      idemKey,
		RequiredCapability:  string(domain.CapabilityMachineStart),
		RequiredScopes:      []string{"machine:write"},
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	_, _, err = mgr.Submit(context.Background(), op2, 30*time.Second)
	if !errors.Is(err, operations.ErrOperationConflict) {
		t.Fatalf("expected ErrOperationConflict on cross-actor same key, got: %v", err)
	}
}

func TestManager_GracefulShutdownAndDrain(t *testing.T) {
	dir := t.TempDir()
	auditDir := t.TempDir()
	rcptDir := t.TempDir()
	leasesDir := t.TempDir()
	approvalsDir := t.TempDir()

	rcptStore := receipt.NewStore(rcptDir)
	auditStore := audit.NewStore(auditDir)
	leaseMgr := lease.NewManager(leasesDir)
	approvalStore := approval.NewStore(approvalsDir)
	eventHub := events.NewHub(dir)

	blockingBackend := &blockingBackend{
		started: make(chan struct{}),
		unblock: make(chan struct{}),
	}
	recoverySvc := app.NewRecoveryService(blockingBackend, leaseMgr, auditStore, rcptStore, approvalStore)
	mgr := operations.NewManager(dir, recoverySvc, rcptStore, auditStore, eventHub)

	act, _ := domain.NewActorContext("user:admin", "user:admin", domain.NewScopeSet("machine:write", "operation:cancel"), domain.NewScopeSet("machine:write", "operation:cancel"))
	target := domain.MachineRef("c4a523d4-6b99-4d62-a5e2-4752c0f20001")

	op := domain.Operation{
		Kind:                "machine.start",
		Target:              target,
		Actor:               act,
		Reason:              "start shutdown test",
		Deadline:            time.Now().Add(time.Minute),
		IdempotencyKey:      "idem-shutdown-1",
		RequiredCapability:  string(domain.CapabilityMachineStart),
		RequiredScopes:      []string{"machine:write"},
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	rec, _, err := mgr.Submit(context.Background(), op, 30*time.Second)
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	<-blockingBackend.started

	// Initiate shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	shutdownStarted := make(chan struct{})
	shutdownDone := make(chan error)
	go func() {
		close(shutdownStarted)
		shutdownDone <- mgr.Shutdown(shutdownCtx)
	}()

	<-shutdownStarted
	time.Sleep(20 * time.Millisecond)

	// Subsequent submissions should be rejected with ErrManagerShuttingDown
	opNew := op
	opNew.IdempotencyKey = "idem-shutdown-new"
	_, _, err = mgr.Submit(context.Background(), opNew, 30*time.Second)
	if !errors.Is(err, operations.ErrManagerShuttingDown) {
		t.Fatalf("expected ErrManagerShuttingDown, got: %v", err)
	}

	close(blockingBackend.unblock)

	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	finalRec, err := mgr.Get(rec.ID, act)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !finalRec.State.IsTerminal() {
		t.Errorf("expected terminal state after shutdown, got: %s", finalRec.State)
	}
}

func TestManager_OfflineCancelWithSyntheticReceipt(t *testing.T) {
	dir := t.TempDir()
	auditDir := t.TempDir()
	rcptDir := t.TempDir()

	rcptStore := receipt.NewStore(rcptDir)
	auditStore := audit.NewStore(auditDir)
	eventHub := events.NewHub(dir)

	mgr := operations.NewManager(dir, nil, rcptStore, auditStore, eventHub)

	act, _ := domain.NewActorContext("user:admin", "user:admin", domain.NewScopeSet("machine:write", "operation:cancel", "audit:read"), domain.NewScopeSet("machine:write", "operation:cancel", "audit:read"))
	target := domain.MachineRef("c4a523d4-6b99-4d62-a5e2-4752c0f20001")
	opID := "op-00000000000000000000000000000099"

	// Create pending on-disk record directly (simulating dead daemon or offline operation)
	rec := domain.OperationRecord{
		SchemaVersion:  "1",
		ID:             opID,
		Actor:          "user:admin",
		Target:         target,
		Kind:           "machine.start",
		RequestedClass: domain.ClassReversibleMutation,
		EffectiveClass: domain.ClassReversibleMutation,
		Fingerprint:    "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		IdempotencyKey: "idem-offline-cancel",
		Deadline:       time.Now().Add(time.Hour),
		State:          domain.OpStatePending,
		CreatedAt:      time.Now().Add(-10 * time.Minute),
	}
	if err := operations.SaveRecord(dir, rec); err != nil {
		t.Fatalf("SaveRecord failed: %v", err)
	}

	// Cancel offline operation
	if err := mgr.Cancel(opID, act, "user requested cancellation"); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}

	updated, err := mgr.Get(opID, act)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if updated.State != domain.OpStateCancelled {
		t.Errorf("expected cancelled state, got %s", updated.State)
	}
	if updated.ReceiptID == "" {
		t.Errorf("expected synthetic receipt ID on cancelled record")
	}

	// Verify synthetic receipt exists and has OutcomeAborted
	rcpt, err := rcptStore.Get(string(updated.ReceiptID))
	if err != nil {
		t.Fatalf("Get receipt failed: %v", err)
	}
	if rcpt.Outcome.Status != domain.OutcomeAborted {
		t.Errorf("expected OutcomeAborted, got %s", rcpt.Outcome.Status)
	}
}
