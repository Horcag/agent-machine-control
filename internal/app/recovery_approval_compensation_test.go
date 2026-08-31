package app_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

var errPreProviderAbort = errors.New("synthetic pre-provider abort")

func issuedTurnOffRequest(t *testing.T, service *app.RecoveryService, root string, idempotencyKey string) (app.MutationRequest, *approval.Store) {
	t.Helper()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	target := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	scopes := domain.NewScopeSet(domain.ScopeMachineWrite)
	actor, err := domain.NewActorContext("operator:direct-compensation", "operator:direct-compensation", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	operation := domain.Operation{
		Kind: "machine.stop", Target: domain.MachineRef(target), Actor: actor,
		Reason: "test direct compensation", Deadline: now.Add(30 * time.Second), IdempotencyKey: idempotencyKey,
		RequiredCapability: domain.CapabilityMachineStop, RequiredScopes: []string{domain.ScopeMachineWrite},
		Classification: domain.ClassDestructivePrivileged, EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters: map[string]any{"mode": "turn-off"},
	}
	fingerprint, err := operation.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	issued := domain.Approval{
		ID: domain.ApprovalID("app-" + idempotencyKey), Actor: actor.EffectiveActor, Target: operation.Target,
		AuthorizedClass: domain.ClassDestructivePrivileged, Fingerprint: fingerprint, IdempotencyKey: idempotencyKey,
		IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
	}
	store := approval.NewStore(filepath.Join(root, "approvals"))
	if err := service.IssueApproval(context.Background(), issued); err != nil {
		t.Fatal(err)
	}
	return app.MutationRequest{
		TargetID: target, Actor: actor, Reason: operation.Reason, IdempotencyKey: idempotencyKey,
		Timeout: 30 * time.Second, Deadline: operation.Deadline, Approval: &issued,
	}, store
}

func TestDirectRecoveryPreProviderAbortReleasesExactApproval(t *testing.T) {
	var providerCalls atomic.Int32
	backend := &mockBackend{
		stopMachineFn: func(context.Context, string, string) (domain.MachineObservation, error) {
			providerCalls.Add(1)
			return domain.MachineObservation{}, nil
		},
	}
	service, root := setupTestRecovery(t, backend)
	request, store := issuedTurnOffRequest(t, service, root, "direct-pre-provider")
	request.OnAdmitted = func(context.Context) error { return errPreProviderAbort }

	if _, _, err := service.StopMachine(context.Background(), request, "turn-off"); !errors.Is(err, errPreProviderAbort) {
		t.Fatalf("StopMachine error = %v, want pre-provider abort", err)
	}
	if providerCalls.Load() != 0 {
		t.Fatalf("provider calls = %d, want zero", providerCalls.Load())
	}
	if consumed, err := store.IsConsumed(string(request.Approval.ID)); err != nil || consumed {
		t.Fatalf("approval consumed after compensated abort = %v, err = %v", consumed, err)
	}

	request.OnAdmitted = nil
	if _, _, err := service.StopMachine(context.Background(), request, "turn-off"); err != nil {
		t.Fatalf("reusing compensated approval failed: %v", err)
	}
	if providerCalls.Load() != 1 {
		t.Fatalf("provider calls after reuse = %d, want one", providerCalls.Load())
	}
}

func TestDirectRecoveryCallerCancellationBeforeProviderReleasesApproval(t *testing.T) {
	var providerCalls atomic.Int32
	backend := &mockBackend{
		stopMachineFn: func(context.Context, string, string) (domain.MachineObservation, error) {
			providerCalls.Add(1)
			return domain.MachineObservation{}, nil
		},
	}
	service, root := setupTestRecovery(t, backend)
	request, store := issuedTurnOffRequest(t, service, root, "direct-cancel-before-provider")
	ctx, cancel := context.WithCancel(context.Background())
	request.OnRunning = func(context.Context) error {
		cancel()
		return nil
	}

	if _, _, err := service.StopMachine(ctx, request, "turn-off"); !errors.Is(err, context.Canceled) {
		t.Fatalf("StopMachine error = %v, want context cancellation", err)
	}
	if providerCalls.Load() != 0 {
		t.Fatalf("provider calls = %d, want zero", providerCalls.Load())
	}
	if consumed, err := store.IsConsumed(string(request.Approval.ID)); err != nil || consumed {
		t.Fatalf("approval consumed after pre-provider cancellation = %v, err = %v", consumed, err)
	}
}

func TestDirectRecoveryProviderStartedFailureKeepsApprovalConsumed(t *testing.T) {
	var providerCalls atomic.Int32
	providerErr := errors.New("synthetic ambiguous provider failure")
	backend := &mockBackend{
		stopMachineFn: func(context.Context, string, string) (domain.MachineObservation, error) {
			providerCalls.Add(1)
			return domain.MachineObservation{}, providerErr
		},
	}
	service, root := setupTestRecovery(t, backend)
	request, store := issuedTurnOffRequest(t, service, root, "direct-provider-started")

	if _, _, err := service.StopMachine(context.Background(), request, "turn-off"); !errors.Is(err, providerErr) {
		t.Fatalf("StopMachine error = %v, want provider failure", err)
	}
	if providerCalls.Load() != 1 {
		t.Fatalf("provider calls = %d, want one", providerCalls.Load())
	}
	if consumed, err := store.IsConsumed(string(request.Approval.ID)); err != nil || !consumed {
		t.Fatalf("approval consumed after ambiguous provider failure = %v, err = %v", consumed, err)
	}
}
