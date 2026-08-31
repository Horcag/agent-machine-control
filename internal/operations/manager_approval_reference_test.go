package operations_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/operations"
)

func TestManagerApprovalReferenceExactRetrySkipsReloadAndDifferentReferenceConflicts(t *testing.T) {
	backend := &mockBackend{}
	mgr, hub, _ := setupTestManager(t, backend)
	actor, err := domain.NewActorContext(
		"operator:local", "operator:local",
		domain.NewScopeSet(domain.ScopeMachineWrite), domain.NewScopeSet(domain.ScopeMachineWrite),
	)
	if err != nil {
		t.Fatal(err)
	}
	op := domain.Operation{
		Kind: "machine.start", Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Actor: actor,
		Reason: "bind approval reference before execution", Deadline: time.Now().UTC().Add(time.Minute),
		IdempotencyKey: "manager-approval-reference", RequiredCapability: string(domain.CapabilityMachineStart),
		RequiredScopes: []string{domain.ScopeMachineWrite}, Classification: domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}
	approvalID := "app-operation-0123456789abcdef0123456789abcdef"
	loads := 0
	rec, existing, err := mgr.SubmitWithApprovalReference(context.Background(), op, time.Minute, approvalID, func(context.Context) (*domain.Approval, error) {
		loads++
		return nil, app.ErrInvalidOperationApprovalReference
	})
	if err != nil || existing {
		t.Fatalf("first submit: rec=%+v existing=%v err=%v", rec, existing, err)
	}
	if rec.ApprovalID != domain.ApprovalID(approvalID) {
		t.Fatalf("record approval_id = %q", rec.ApprovalID)
	}
	ch, unsubscribe, err := hub.Subscribe(context.Background(), rec.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	for event := range ch {
		if event.State.IsTerminal() {
			break
		}
	}

	retry, existing, err := mgr.SubmitWithApprovalReference(context.Background(), op, time.Minute, approvalID, func(context.Context) (*domain.Approval, error) {
		loads++
		t.Fatal("exact terminal retry reloaded approval authority")
		return nil, nil
	})
	if err != nil || !existing || retry.ID != rec.ID {
		t.Fatalf("exact retry: rec=%+v existing=%v err=%v", retry, existing, err)
	}
	if loads != 1 {
		t.Fatalf("approval resolver calls = %d, want 1", loads)
	}

	otherID := "app-operation-fedcba9876543210fedcba9876543210"
	if _, _, err := mgr.SubmitWithApprovalReference(context.Background(), op, time.Minute, otherID, func(context.Context) (*domain.Approval, error) {
		t.Fatal("conflicting approval reference was resolved")
		return nil, nil
	}); !errors.Is(err, operations.ErrOperationConflict) {
		t.Fatalf("different approval reference error = %v", err)
	}
	if backend.startCount.Load() != 0 {
		t.Fatalf("invalid approval reference reached backend %d times", backend.startCount.Load())
	}
}

func TestManagerApprovalReferenceValidatesResolverAndContext(t *testing.T) {
	backend := &mockBackend{}
	mgr, hub, _ := setupTestManager(t, backend)
	actor, _ := domain.NewActorContext(
		"operator:local", "operator:local",
		domain.NewScopeSet(domain.ScopeMachineWrite), domain.NewScopeSet(domain.ScopeMachineWrite),
	)
	op := domain.Operation{
		Kind: "machine.start", Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Actor: actor,
		Reason: "validate approval resolver", Deadline: time.Now().UTC().Add(time.Minute),
		IdempotencyKey: "validate-approval-resolver", RequiredCapability: string(domain.CapabilityMachineStart),
		RequiredScopes: []string{domain.ScopeMachineWrite}, Classification: domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}
	if _, _, err := mgr.SubmitWithApprovalReference(context.Background(), op, time.Minute, "../bad", func(context.Context) (*domain.Approval, error) { return nil, nil }); err == nil {
		t.Fatal("invalid approval ID was accepted")
	}
	validID := "app-operation-0123456789abcdef0123456789abcdef"
	if _, _, err := mgr.SubmitWithApprovalReference(context.Background(), op, time.Minute, validID, nil); !errors.Is(err, domain.ErrInvalidApprovalRecord) {
		t.Fatalf("nil resolver error = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := mgr.SubmitWithApprovalReference(cancelled, op, time.Minute, validID, func(context.Context) (*domain.Approval, error) { return nil, nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled resolver error = %v", err)
	}

	op.IdempotencyKey = "wrong-resolved-approval-id"
	record, _, err := mgr.SubmitWithApprovalReference(context.Background(), op, time.Minute, validID, func(context.Context) (*domain.Approval, error) {
		return &domain.Approval{ID: "app-operation-fedcba9876543210fedcba9876543210"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	events, unsubscribe, err := hub.Subscribe(context.Background(), record.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	for event := range events {
		if event.State.IsTerminal() {
			break
		}
	}
	terminal, err := mgr.Get(record.ID, actor)
	if err != nil || terminal.ErrorCategory != "approval_record_mismatch" || backend.startCount.Load() != 0 {
		t.Fatalf("terminal=%+v err=%v backend=%d", terminal, err, backend.startCount.Load())
	}
}
