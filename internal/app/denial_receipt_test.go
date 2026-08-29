package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/policy"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func assertReceiptAndError(t *testing.T, rcpt domain.Receipt, err error, deniedErr **app.PolicyDeniedError, mutationCalls int) {
	t.Helper()
	if err == nil {
		t.Fatal("expected policy denial")
	}

	var dErr *app.PolicyDeniedError
	if !errors.As(err, &dErr) {
		t.Fatalf("expected PolicyDeniedError, got %v", err)
	}
	*deniedErr = dErr

	if rcpt.ReceiptID == "" {
		t.Fatalf("expected receipt ID for denial, but got error: %v", err)
	}
	if rcpt.Outcome.Status != domain.OutcomeDenied {
		t.Fatalf("expected OutcomeDenied, got %v", rcpt.Outcome.Status)
	}
	if rcpt.Outcome.ErrorCategory != string(dErr.Reason) {
		t.Fatalf("expected error category %s, got %s", dErr.Reason, rcpt.Outcome.ErrorCategory)
	}
	if rcpt.Outcome.ErrorMessage != dErr.Message {
		t.Fatalf("expected error message %s, got %s", dErr.Message, rcpt.Outcome.ErrorMessage)
	}
	if mutationCalls != 0 {
		t.Fatalf("expected 0 provider mutation calls, got %d", mutationCalls)
	}
}

func assertAuditEvent(t *testing.T, auditStore *audit.Store, expectedReceiptID string, expectedStatus string, expectedCategory string, expectedMessage string) {
	t.Helper()
	events, err := auditStore.Tail(10)
	if err != nil {
		t.Fatalf("audit tail failed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected exactly 1 audit event, got %d", len(events))
	}
	e := events[0]
	if e.ReceiptID != expectedReceiptID || e.OutcomeStatus != domain.OutcomeStatus(expectedStatus) || e.ErrorCategory != expectedCategory || e.ErrorMessage != expectedMessage {
		t.Fatalf("audit event mismatch: %+v", e)
	}
}

func assertIdempotencyRetry(t *testing.T, svc *app.RecoveryService, req app.MutationRequest, rcpt domain.Receipt, mutationCalls *int, auditStore *audit.Store) {
	t.Helper()
	rcpt2, _, err2 := svc.StartMachine(context.Background(), req)
	var deniedErr2 *app.PolicyDeniedError
	if !errors.As(err2, &deniedErr2) {
		t.Fatalf("expected PolicyDeniedError on retry, got %v", err2)
	}
	if deniedErr2.Reason != policy.DenialReason(rcpt.Outcome.ErrorCategory) || deniedErr2.Message != rcpt.Outcome.ErrorMessage {
		t.Fatalf("expected reconstructed PolicyDeniedError on retry")
	}
	if rcpt2.ReceiptID != rcpt.ReceiptID {
		t.Fatalf("expected same receipt ID on retry")
	}

	if *mutationCalls != 0 {
		t.Fatalf("expected 0 provider mutation calls on retry, got %d", *mutationCalls)
	}
	events2, err := auditStore.Tail(10)
	if err != nil {
		t.Fatalf("audit tail on retry failed: %v", err)
	}
	if len(events2) != 1 {
		t.Fatalf("expected exactly 1 audit event after retry, got %d", len(events2))
	}
}

func TestDenialReceiptPersistence(t *testing.T) {
	dir := t.TempDir()
	rcptDir := filepath.Join(dir, "receipts")
	auditDir := filepath.Join(dir, "audit")

	if err := os.Mkdir(rcptDir, 0700); err != nil {
		t.Fatalf("failed to create receipt dir: %v", err)
	}
	if err := os.Mkdir(auditDir, 0700); err != nil {
		t.Fatalf("failed to create audit dir: %v", err)
	}

	rcptStore := receipt.NewStore(rcptDir)
	auditStore := audit.NewStore(auditDir)

	var mutationCalls int
	backend := &mockBackend{
		capabilitiesFn: func(_ context.Context, _ string) (domain.CapabilitySet, error) {
			return domain.CapabilitySet{}, nil
		},
		listCheckpointsFn: func(_ context.Context, _ string) ([]domain.CheckpointObservation, error) {
			return nil, nil
		},
		startMachineFn: func(_ context.Context, _ string) (domain.MachineObservation, error) {
			mutationCalls++
			return domain.MachineObservation{}, nil
		},
	}

	svc := app.NewRecoveryService(
		backend,
		lease.NewManager(filepath.Join(dir, "leases")),
		auditStore,
		rcptStore,
		approval.NewStore(filepath.Join(dir, "approvals")),
	)

	req := app.MutationRequest{
		TargetID:       "vm-12345678-1234-1234-1234-123456789012",
		Actor:          domain.ActorContext{AuthenticatedCaller: "user:test", EffectiveActor: "user:test"},
		Reason:         "test",
		IdempotencyKey: "test-key-1",
		Timeout:        time.Minute,
		Deadline:       time.Now().Add(time.Minute),
	}

	rcpt, _, err := svc.StartMachine(context.Background(), req)
	var deniedErr *app.PolicyDeniedError
	assertReceiptAndError(t, rcpt, err, &deniedErr, mutationCalls)

	// Receipt retrievable from store
	q := rcptStore
	storedRcpt, err := q.Get(string(rcpt.ReceiptID))
	if err != nil {
		t.Fatalf("failed to retrieve receipt from store: %v", err)
	}
	if storedRcpt.ReceiptID != rcpt.ReceiptID {
		t.Fatalf("stored receipt mismatch")
	}

	assertAuditEvent(t, auditStore, string(rcpt.ReceiptID), "denied", string(deniedErr.Reason), deniedErr.Message)
	assertIdempotencyRetry(t, svc, req, rcpt, &mutationCalls, auditStore)
}

func TestDenialReceiptSaveFailure(t *testing.T) {
	dir := t.TempDir()
	rcptDir := filepath.Join(dir, "receipts")
	auditDir := filepath.Join(dir, "audit")
	if err := os.Mkdir(rcptDir, 0700); err != nil {
		t.Fatalf("failed to create receipts dir: %v", err)
	}
	if err := os.Mkdir(auditDir, 0700); err != nil {
		t.Fatalf("failed to create audit dir: %v", err)
	}

	rcptStore := receipt.NewStore(rcptDir, receipt.WithSyncDir(func(_ string) error {
		return errors.New("simulated save error")
	}))

	backend := &mockBackend{
		capabilitiesFn: func(_ context.Context, _ string) (domain.CapabilitySet, error) {
			return domain.CapabilitySet{}, nil
		},
		listCheckpointsFn: func(_ context.Context, _ string) ([]domain.CheckpointObservation, error) {
			return nil, nil
		},
	}
	svc := app.NewRecoveryService(backend, lease.NewManager(filepath.Join(dir, "leases")), audit.NewStore(auditDir), rcptStore, approval.NewStore(filepath.Join(dir, "approvals")))
	req := app.MutationRequest{TargetID: "vm-12345678-1234-1234-1234-123456789012", Actor: domain.ActorContext{AuthenticatedCaller: "user", EffectiveActor: "user"}, Reason: "test", IdempotencyKey: "key-1", Timeout: time.Minute, Deadline: time.Now().Add(time.Minute)}

	rcpt, _, err := svc.StartMachine(context.Background(), req)
	if rcpt.ReceiptID != "" {
		t.Fatalf("expected no receipt ID on save failure, got %s", rcpt.ReceiptID)
	}
	if err == nil {
		t.Fatalf("expected error on save failure")
	}
}

func TestDenialAuditFailure(t *testing.T) {
	dir := t.TempDir()
	rcptDir := filepath.Join(dir, "receipts")
	auditDir := filepath.Join(dir, "audit")
	if err := os.Mkdir(rcptDir, 0700); err != nil {
		t.Fatalf("failed to create receipts dir: %v", err)
	}
	if err := os.Mkdir(auditDir, 0700); err != nil {
		t.Fatalf("failed to create audit dir: %v", err)
	} // Writable so CheckWritable passes

	var syncCalled bool
	auditStore := audit.NewStore(auditDir, audit.WithSyncDir(func(_ string) error {
		syncCalled = true
		return errors.New("simulated sync error")
	}))

	backend := &mockBackend{
		capabilitiesFn: func(_ context.Context, _ string) (domain.CapabilitySet, error) {
			return domain.CapabilitySet{}, nil
		},
		listCheckpointsFn: func(_ context.Context, _ string) ([]domain.CheckpointObservation, error) {
			return nil, nil
		},
	}
	svc := app.NewRecoveryService(backend, lease.NewManager(filepath.Join(dir, "leases")), auditStore, receipt.NewStore(rcptDir), approval.NewStore(filepath.Join(dir, "approvals")))
	req := app.MutationRequest{TargetID: "vm-12345678-1234-1234-1234-123456789012", Actor: domain.ActorContext{AuthenticatedCaller: "user", EffectiveActor: "user"}, Reason: "test", IdempotencyKey: "key-1", Timeout: time.Minute, Deadline: time.Now().Add(time.Minute)}

	rcpt, _, err := svc.StartMachine(context.Background(), req)
	if rcpt.ReceiptID == "" {
		t.Fatalf("expected real receipt ID on audit failure, got %v", rcpt)
	}
	if err == nil {
		t.Fatalf("expected error on audit failure")
	}
	if !syncCalled {
		t.Fatalf("expected sync error to be triggered")
	}
	var deniedErr *app.PolicyDeniedError
	if !errors.As(err, &deniedErr) {
		t.Fatalf("expected PolicyDeniedError joined with audit error, got %v", err)
	}
}
