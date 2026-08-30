package app_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/Horcag/agent-machine-control/internal/target"
)

func TestRecoveryServiceCanonicalizesAllPublicTargetReferencesBeforeEffects(t *testing.T) {
	const vmID = "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	const checkpointID = "c4a523d4-6b99-4d62-a5e2-4752c0f20002"
	now := time.Date(2026, 8, 31, 4, 0, 0, 0, time.UTC)
	locator, err := domain.NewMachineLocator(domain.LocalHostID, vmID)
	if err != nil {
		t.Fatal(err)
	}
	observation := domain.MachineObservation{
		HostID: domain.LocalHostID, Locator: locator, ID: vmID, Name: "private display name",
		State: domain.MachineStateOff, RawState: "Off", Generation: 2, Version: "10.0",
		MemoryAssignedBytes: 1024, Capabilities: domain.DirectMachineCapabilities(), ObservedAt: now,
		ObservationType: domain.ObservationObserved,
	}
	startCalls := 0
	backend := &mockBackend{
		listMachinesFn: func(context.Context) ([]domain.MachineObservation, error) {
			return []domain.MachineObservation{observation}, nil
		},
		listCheckpointsFn: func(_ context.Context, targetID string) ([]domain.CheckpointObservation, error) {
			if targetID != vmID {
				t.Fatalf("ListCheckpoints target = %q", targetID)
			}
			return []domain.CheckpointObservation{{
				ID: checkpointID, Name: "rollback", VMID: vmID, CreatedAt: now, ObservedAt: now,
				ObservationType: domain.ObservationObserved,
			}}, nil
		},
		capabilitiesFn: func(_ context.Context, targetID string) (domain.CapabilitySet, error) {
			if targetID != vmID {
				t.Fatalf("Capabilities target = %q", targetID)
			}
			return domain.DirectMachineCapabilities(), nil
		},
		startMachineFn: func(_ context.Context, targetID string) (domain.MachineObservation, error) {
			startCalls++
			if targetID != vmID {
				t.Fatalf("StartMachine target = %q", targetID)
			}
			return observation, nil
		},
	}

	state, err := statedir.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := state.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	inventory, err := app.NewTrustedInventory(nil)
	if err != nil {
		t.Fatal(err)
	}
	targetStore, err := target.NewStore(state.TargetsDir())
	if err != nil {
		t.Fatal(err)
	}
	refresh := func(ctx context.Context) error {
		_, err := app.RefreshTrustedInventory(ctx, inventory, func(app.HostEntry) app.TrustedHostObserver { return backend }, 1)
		return err
	}
	targetService, err := app.NewTargetService(inventory, targetStore, app.WithTargetRefresh(refresh))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := targetService.EnrollDefaultTarget(context.Background(), "", []string{"primary"}); err != nil {
		t.Fatalf("EnrollDefaultTarget: %v", err)
	}

	leaseManager := lease.NewManager(state.LeasesDir(), lease.WithClock(func() time.Time { return now }))
	auditStore := audit.NewStore(state.AuditDir(), audit.WithClock(func() time.Time { return now }))
	receiptStore := receipt.NewStore(state.ReceiptsDir())
	approvalStore := approval.NewStore(state.ApprovalsDir())
	recovery := app.NewRecoveryService(
		backend, leaseManager, auditStore, receiptStore, approvalStore,
		app.WithRecoveryClock(func() time.Time { return now }),
		app.WithRecoveryTargetResolver(targetService),
	)
	scopes := domain.NewScopeSet(domain.ScopeMachineWrite)
	actor, err := domain.NewActorContext("operator:test", "operator:test", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	request := app.MutationRequest{
		TargetID: "default", Actor: actor, Reason: "canonical identity test",
		IdempotencyKey: "canonical-target-retry", Timeout: 30 * time.Second,
	}
	first, _, err := recovery.StartMachine(context.Background(), request)
	if err != nil {
		t.Fatalf("StartMachine default: %v", err)
	}
	if first.Target != domain.MachineRef(locator.String()) {
		t.Fatalf("receipt target = %q", first.Target)
	}
	for _, reference := range []string{"primary", vmID, locator.String()} {
		request.TargetID = reference
		retry, _, err := recovery.StartMachine(context.Background(), request)
		if err != nil {
			t.Fatalf("StartMachine %q: %v", reference, err)
		}
		if retry.ReceiptID != first.ReceiptID {
			t.Fatalf("retry receipt %q != %q", retry.ReceiptID, first.ReceiptID)
		}
	}
	if startCalls != 1 {
		t.Fatalf("StartMachine calls = %d", startCalls)
	}
	if _, err := recovery.ListCheckpoints(context.Background(), "primary"); err != nil {
		t.Fatalf("ListCheckpoints alias: %v", err)
	}
	if entries, err := os.ReadDir(state.ReceiptsDir()); err != nil || len(entries) != 1 {
		t.Fatalf("receipt files = %d, %v", len(entries), err)
	}
}
