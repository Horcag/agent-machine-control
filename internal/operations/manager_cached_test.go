package operations_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

func TestManager_CachedReceiptOutcomes(t *testing.T) {
	b := &mockBackend{}
	dir := t.TempDir()
	leasesDir := t.TempDir()
	auditDir := t.TempDir()
	rcptDir := t.TempDir()
	apprDir := t.TempDir()

	leaseMgr := lease.NewManager(leasesDir)
	auditStore := audit.NewStore(auditDir)
	rcptStore := receipt.NewStore(rcptDir)
	apprStore := approval.NewStore(apprDir)
	eventHub := events.NewHub(dir)

	recoverySvc := app.NewRecoveryService(b, leaseMgr, auditStore, rcptStore, apprStore)
	mgr := operations.NewManager(dir, recoverySvc, rcptStore, auditStore, eventHub)
	t.Cleanup(func() {
		_ = mgr.Shutdown(context.Background())
	})

	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	act := domain.ActorContext{
		AuthenticatedCaller:  "operator:local",
		EffectiveActor:       "operator:local",
		CallerPermissions:    domain.NewScopeSet("machine:write"),
		EffectivePermissions: domain.NewScopeSet("machine:write"),
	}

	op := domain.Operation{
		Kind:                "machine.start",
		Target:              domain.MachineRef(targetID),
		Actor:               act,
		Reason:              "test start",
		Deadline:            time.Now().Add(time.Minute),
		IdempotencyKey:      "key-cached-receipt",
		RequiredCapability:  string(domain.CapabilityMachineStart),
		RequiredScopes:      []string{"machine:write"},
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	cases := []struct {
		name          string
		outcomeStatus domain.OutcomeStatus
		errorCategory string
		errorMessage  string
		expectedState domain.OperationState
	}{
		{
			name:          "success outcome maps to completed state",
			outcomeStatus: domain.OutcomeSuccess,
			expectedState: domain.OpStateCompleted,
		},
		{
			name:          "denied outcome maps to failed state and restores errors",
			outcomeStatus: domain.OutcomeDenied,
			errorCategory: "policy_denial",
			errorMessage:  "operation was denied by policy",
			expectedState: domain.OpStateFailed,
		},
		{
			name:          "failed outcome maps to failed state",
			outcomeStatus: domain.OutcomeFailed,
			expectedState: domain.OpStateFailed,
		},
		{
			name:          "aborted outcome maps to cancelled state",
			outcomeStatus: domain.OutcomeAborted,
			expectedState: domain.OpStateCancelled,
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runCachedReceiptTestCase(t, i, tc, op, act, dir, recoverySvc, rcptStore, auditStore, eventHub, mgr)
		})
	}
}

func runCachedReceiptTestCase(t *testing.T, i int, tc struct {
	name          string
	outcomeStatus domain.OutcomeStatus
	errorCategory string
	errorMessage  string
	expectedState domain.OperationState
}, op domain.Operation, act domain.ActorContext, dir string, recoverySvc *app.RecoveryService, rcptStore *receipt.Store, auditStore *audit.Store, eventHub *events.Hub, mgr *operations.Manager) {
	testOp := op
	testOp.IdempotencyKey = fmt.Sprintf("key-cached-%d", i)

	fp, err := testOp.Fingerprint()
	if err != nil {
		t.Fatalf("failed to compute fingerprint: %v", err)
	}
	idFp, err := domain.ComputeIdempotencyFingerprint(testOp)
	if err != nil {
		t.Fatalf("failed to compute idempotency fingerprint: %v", err)
	}

	now := time.Now().Truncate(time.Second)
	rcpt := domain.Receipt{
		ReceiptID:              domain.ReceiptID(fmt.Sprintf("rcpt-0000000000000000000000000000%04d", i)),
		OperationKind:          testOp.Kind,
		Fingerprint:            fp,
		IdempotencyFingerprint: idFp,
		IdempotencyKey:         testOp.IdempotencyKey,
		Actor:                  act.EffectiveActor,
		Target:                 testOp.Target,
		Class:                  testOp.Classification,
		EffectiveBackend:       "hyperv",
		StartedAt:              now,
		CompletedAt:            now.Add(2 * time.Second),
		Outcome: domain.ExecutionOutcome{
			Status:        tc.outcomeStatus,
			ErrorCategory: tc.errorCategory,
			ErrorMessage:  tc.errorMessage,
		},
		ObservationType: domain.ObservationObserved,
		RedactionStatus: domain.RedactionApplied,
		RollbackRef:     "chk-1",
	}

	if i == 0 {
		rcpt.IdempotencyFingerprint = ""
	}

	if err := rcptStore.Save(rcpt); err != nil {
		t.Fatalf("failed to save receipt: %v", err)
	}

	// Test that when we submit a retry/duplicate request with a regenerated deadline
	// it still matches via IdempotencyFingerprint and is successfully recovered.
	if i != 0 {
		testOp.Deadline = time.Now().Add(2 * time.Minute) // Regenerated deadline
	}

	rec, wasExisting, err := mgr.Submit(context.Background(), testOp, 30*time.Second)
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}
	if !wasExisting {
		t.Errorf("expected existing record reconstructed from cache, got new submission")
	}

	assertRecordFields(t, rec, tc.expectedState, rcpt.ReceiptID, tc.errorCategory, tc.errorMessage)

	// Create a fresh manager/store and try to Get/List the reconstructed record.
	mgrFresh := operations.NewManager(dir, recoverySvc, rcptStore, auditStore, eventHub)
	verifyGetAndList(t, mgrFresh, rec, act, rcpt.ReceiptID, tc.expectedState, tc.errorCategory, tc.errorMessage)

	// Verify that the same receipt reconstructs the same ID on repeated manager creation/restart.
	mgrRestarted := operations.NewManager(dir, recoverySvc, rcptStore, auditStore, eventHub)
	recRestarted, wasExistingRestarted, err := mgrRestarted.Submit(context.Background(), testOp, 30*time.Second)
	if err != nil {
		t.Fatalf("Submit on restarted manager failed: %v", err)
	}
	if !wasExistingRestarted {
		t.Errorf("expected existing record reconstructed from cache on restarted manager, got new submission")
	}
	if recRestarted.ID != rec.ID {
		t.Errorf("expected same reconstructed operation ID after manager restart, got %q and %q", rec.ID, recRestarted.ID)
	}
	assertRecordFields(t, recRestarted, tc.expectedState, rcpt.ReceiptID, tc.errorCategory, tc.errorMessage)

	// Add a semantic-change collision assertion
	collisionOp := testOp
	collisionOp.Kind = "machine.stop" // Change semantic fingerprint
	_, _, err = mgr.Submit(context.Background(), collisionOp, 30*time.Second)
	if !errors.Is(err, operations.ErrOperationConflict) {
		t.Errorf("expected ErrOperationConflict for semantic change, got %v", err)
	}

	// Test manager with empty directory (m.dir == "") returns ErrPersistenceFailure
	mgrEmptyDir := operations.NewManager("", recoverySvc, rcptStore, auditStore, eventHub)
	_, _, err = mgrEmptyDir.Submit(context.Background(), testOp, 30*time.Second)
	if !errors.Is(err, operations.ErrPersistenceFailure) {
		t.Errorf("expected ErrPersistenceFailure for empty dir, got %v", err)
	}

	// Run persistence edge-case coverage tests
	verifyPersistenceEdgeCases(t, mgr, dir, rec, testOp)
}

func verifyPersistenceEdgeCases(t *testing.T, mgr *operations.Manager, dir string, rec *domain.OperationRecord, testOp domain.Operation) {
	t.Helper()

	// 1. Test incompatible existing record on disk (returns ErrOperationConflict)
	incompRec := *rec
	incompRec.Actor = "different-actor"
	if err := operations.SaveRecord(dir, incompRec); err != nil {
		t.Fatalf("failed to save incompatible record: %v", err)
	}
	_, _, err := mgr.Submit(context.Background(), testOp, 30*time.Second)
	if !errors.Is(err, operations.ErrOperationConflict) {
		t.Errorf("expected ErrOperationConflict for incompatible record on disk, got %v", err)
	}
	if err := os.Remove(filepath.Join(dir, fmt.Sprintf("%s.json", rec.ID))); err != nil {
		t.Fatalf("failed to remove incompatible record file: %v", err)
	}

	// 2. Test corrupted/invalid JSON record on disk (returns read error != ErrOperationNotFound)
	recPath := filepath.Join(dir, fmt.Sprintf("%s.json", rec.ID))
	if err := os.WriteFile(recPath, []byte("{invalid-json"), 0600); err != nil {
		t.Fatalf("failed to write corrupted file: %v", err)
	}
	_, _, err = mgr.Submit(context.Background(), testOp, 30*time.Second)
	if err == nil {
		t.Errorf("expected error for corrupted record on disk, got nil")
	}
	if err := os.Remove(recPath); err != nil {
		t.Fatalf("failed to remove corrupted record file: %v", err)
	}
}

func verifyGetAndList(t *testing.T, mgrFresh *operations.Manager, rec *domain.OperationRecord, act domain.ActorContext, rcptID domain.ReceiptID, expectedState domain.OperationState, errorCategory, errorMessage string) {
	t.Helper()
	recGet, err := mgrFresh.Get(rec.ID, act)
	if err != nil {
		t.Fatalf("Get on fresh manager failed: %v", err)
	}
	if recGet.ID != rec.ID {
		t.Errorf("expected Get to return same ID %q, got %q", rec.ID, recGet.ID)
	}
	assertRecordFields(t, recGet, expectedState, rcptID, errorCategory, errorMessage)

	listOpts := operations.ListOptions{}
	recList, err := mgrFresh.List(listOpts, act)
	if err != nil {
		t.Fatalf("List on fresh manager failed: %v", err)
	}
	foundInList := false
	for _, r := range recList {
		if r.ID == rec.ID {
			foundInList = true
			assertRecordFields(t, &r, expectedState, rcptID, errorCategory, errorMessage)
		}
	}
	if !foundInList {
		t.Errorf("expected reconstructed record %q to be in the list, but it wasn't", rec.ID)
	}
}

func assertRecordFields(t *testing.T, rec *domain.OperationRecord, expectedState domain.OperationState, receiptID domain.ReceiptID, errorCategory, errorMessage string) {
	t.Helper()
	if err := domain.ValidateOperationID(rec.ID); err != nil {
		t.Errorf("expected derived operation ID %q to pass validation, got: %v", rec.ID, err)
	}
	if err := rec.Validate(); err != nil {
		t.Errorf("expected OperationRecord to pass Validate(), got: %v", err)
	}
	if rec.State != expectedState {
		t.Errorf("expected state %s, got %s", expectedState, rec.State)
	}
	if rec.ReceiptID != receiptID {
		t.Errorf("expected receipt ID %s, got %s", receiptID, rec.ReceiptID)
	}
	if rec.ErrorCategory != errorCategory {
		t.Errorf("expected error category %q, got %q", errorCategory, rec.ErrorCategory)
	}
	if rec.ErrorMessage != errorMessage {
		t.Errorf("expected error message %q, got %q", errorMessage, rec.ErrorMessage)
	}
}
