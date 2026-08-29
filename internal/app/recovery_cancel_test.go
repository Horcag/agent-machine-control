package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestRecoveryService_CancelledContext_LeaseCleanedUp(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	snapID := "e4a523d4-6b99-4d62-a5e2-4752c0f20001"

	ctx, cancel := context.WithCancel(context.Background())

	backend := &mockBackend{
		listCheckpointsFn: func(_ context.Context, id string) ([]domain.CheckpointObservation, error) {
			return []domain.CheckpointObservation{
				{
					ID:              snapID,
					Name:            "baseline-snap",
					VMID:            id,
					CheckpointType:  "Standard",
					CreatedAt:       time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC),
					ObservedAt:      time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC),
					ObservationType: domain.ObservationObserved,
				},
			}, nil
		},
		startMachineFn: func(_ context.Context, _ string) (domain.MachineObservation, error) {
			// Cancel caller context during execution!
			cancel()
			return domain.MachineObservation{}, context.Canceled
		},
	}

	svc, dir := setupTestRecovery(t, backend)
	actor, _ := domain.NewActorContext("user:admin", "user:admin", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))

	req := app.MutationRequest{
		TargetID:       targetID,
		Actor:          actor,
		Reason:         "test cancel lease cleanup",
		IdempotencyKey: "key-cancel-lease",
		Timeout:        30 * time.Second,
	}

	_, _, err := svc.StartMachine(ctx, req)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	// Verify lease file was cleanly removed despite cancelled context
	leaseFile := filepath.Join(dir, "leases", targetID+".lease.json")
	if _, err := os.Stat(leaseFile); !os.IsNotExist(err) {
		t.Fatalf("expected lease file %s to be removed, but it still exists", leaseFile)
	}
}
