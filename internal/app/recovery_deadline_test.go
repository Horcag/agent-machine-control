package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestDirectRecoveryRollbackDeadlinePersistsAbortAndSkipsProvider(t *testing.T) {
	var providerCalls atomic.Int32
	backend := &mockBackend{
		listCheckpointsFn: func(ctx context.Context, _ string) ([]domain.CheckpointObservation, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		startMachineFn: func(context.Context, string) (domain.MachineObservation, error) {
			providerCalls.Add(1)
			return domain.MachineObservation{}, nil
		},
	}
	service, _ := setupTestRecovery(t, backend)
	request := directDeadlineRequest("direct-rollback-deadline", 80*time.Millisecond, 5*time.Second)

	started := time.Now()
	receipt, _, err := service.StartMachine(context.Background(), request)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("StartMachine error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("direct deadline completion took %s", elapsed)
	}
	if receipt.ReceiptID == "" || receipt.Outcome.Status != domain.OutcomeAborted || receipt.Outcome.ErrorCategory != "deadline_exceeded" {
		t.Fatalf("timeout receipt = %+v", receipt)
	}
	if providerCalls.Load() != 0 {
		t.Fatalf("provider calls = %d, want zero", providerCalls.Load())
	}

	request.Deadline = time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC).Add(time.Second)
	retry, _, err := service.StartMachine(context.Background(), request)
	if !errors.Is(err, context.DeadlineExceeded) || retry.ReceiptID != receipt.ReceiptID {
		t.Fatalf("retry receipt = %s error = %v, want cached timeout %s", retry.ReceiptID, err, receipt.ReceiptID)
	}
	if providerCalls.Load() != 0 {
		t.Fatalf("retry provider calls = %d, want zero", providerCalls.Load())
	}
}

func TestDirectRecoveryProviderReceivesRemainingLifecycleBudget(t *testing.T) {
	var admissionDeadline time.Time
	var providerDeadline time.Time
	var providerRemaining time.Duration
	backend := &mockBackend{
		listCheckpointsFn: func(_ context.Context, id string) ([]domain.CheckpointObservation, error) {
			return []domain.CheckpointObservation{{
				ID: "e4a523d4-6b99-4d62-a5e2-4752c0f20001", Name: "baseline", VMID: id,
				CheckpointType: "Standard", CreatedAt: time.Now(), ObservedAt: time.Now(), ObservationType: domain.ObservationObserved,
			}}, nil
		},
		capabilitiesFn: func(ctx context.Context, _ string) (domain.CapabilitySet, error) {
			admissionDeadline, _ = ctx.Deadline()
			time.Sleep(100 * time.Millisecond)
			return domain.DirectMachineCapabilities(), nil
		},
		startMachineFn: func(ctx context.Context, id string) (domain.MachineObservation, error) {
			providerDeadline, _ = ctx.Deadline()
			providerRemaining = time.Until(providerDeadline)
			return domain.MachineObservation{ID: id, State: domain.MachineStateRunning}, nil
		},
	}
	service, _ := setupTestRecovery(t, backend)
	request := directDeadlineRequest("direct-remaining-budget", 2*time.Second, 5*time.Second)

	if _, _, err := service.StartMachine(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if admissionDeadline.IsZero() || providerDeadline.IsZero() || !providerDeadline.Equal(admissionDeadline) {
		t.Fatalf("admission deadline = %v provider deadline = %v", admissionDeadline, providerDeadline)
	}
	if providerRemaining <= 0 || providerRemaining >= 1950*time.Millisecond {
		t.Fatalf("provider remaining budget = %s, want consumed lifecycle budget", providerRemaining)
	}
}

func TestDirectRecoveryDoesNotRenewExpiredOperationDeadline(t *testing.T) {
	var providerCalls atomic.Int32
	backend := &mockBackend{
		listCheckpointsFn: func(context.Context, string) ([]domain.CheckpointObservation, error) {
			t.Fatal("rollback lookup ran after operation deadline")
			return nil, nil
		},
		startMachineFn: func(context.Context, string) (domain.MachineObservation, error) {
			providerCalls.Add(1)
			return domain.MachineObservation{}, nil
		},
	}
	service, _ := setupTestRecovery(t, backend)
	request := directDeadlineRequest("direct-expired-deadline", -time.Millisecond, 5*time.Second)

	receipt, _, err := service.StartMachine(context.Background(), request)
	if !errors.Is(err, context.DeadlineExceeded) || receipt.Outcome.ErrorCategory != "deadline_exceeded" {
		t.Fatalf("expired operation result = (%+v, %v)", receipt, err)
	}
	if providerCalls.Load() != 0 {
		t.Fatalf("provider calls = %d, want zero", providerCalls.Load())
	}
}

func TestDirectRecoveryDeadlineAfterAdmissionCompensatesApprovalAndLease(t *testing.T) {
	var providerCalls atomic.Int32
	backend := &mockBackend{
		stopMachineFn: func(context.Context, string, string) (domain.MachineObservation, error) {
			providerCalls.Add(1)
			return domain.MachineObservation{}, nil
		},
	}
	service, root := setupTestRecovery(t, backend)
	request, approvalStore := issuedTurnOffRequest(t, service, root, "direct-deadline-compensation")
	request.Timeout = 80 * time.Millisecond
	request.OnRunning = func(ctx context.Context) error {
		<-ctx.Done()
		return nil
	}

	receipt, _, err := service.StopMachine(context.Background(), request, "turn-off")
	if !errors.Is(err, context.DeadlineExceeded) || receipt.Outcome.ErrorCategory != "deadline_exceeded" {
		t.Fatalf("deadline result = (%+v, %v)", receipt, err)
	}
	if providerCalls.Load() != 0 {
		t.Fatalf("provider calls = %d, want zero", providerCalls.Load())
	}
	if consumed, err := approvalStore.IsConsumed(string(request.Approval.ID)); err != nil || consumed {
		t.Fatalf("approval consumed after zero-effect timeout = %v, err = %v", consumed, err)
	}
	leasePath := filepath.Join(root, "leases", request.TargetID+".lease.json")
	if _, err := os.Stat(leasePath); !os.IsNotExist(err) {
		t.Fatalf("lease remains after zero-effect timeout: %v", err)
	}
}

func directDeadlineRequest(key string, deadlineBudget, timeout time.Duration) app.MutationRequest {
	scopes := domain.NewScopeSet(domain.ScopeMachineWrite)
	actor, _ := domain.NewActorContext("operator:direct-deadline", "operator:direct-deadline", scopes, scopes)
	return app.MutationRequest{
		TargetID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Actor: actor,
		Reason: "verify direct deadline", IdempotencyKey: key, Timeout: timeout,
		Deadline: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC).Add(deadlineBudget),
	}
}
