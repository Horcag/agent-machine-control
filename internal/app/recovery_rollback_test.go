package app_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func TestRecoveryService_Rollback_MalformedCandidate_FailsClosed(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		chk  domain.CheckpointObservation
	}{
		{
			name: "missing name",
			chk: domain.CheckpointObservation{
				ID:              "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
				VMID:            targetID,
				CreatedAt:       now,
				ObservedAt:      now,
				ObservationType: domain.ObservationObserved,
			},
		},
		{
			name: "invalid checkpoint GUID",
			chk: domain.CheckpointObservation{
				ID:              "not-a-valid-guid",
				Name:            "bad-guid-snap",
				VMID:            targetID,
				CreatedAt:       now,
				ObservedAt:      now,
				ObservationType: domain.ObservationObserved,
			},
		},
		{
			name: "zero created_at",
			chk: domain.CheckpointObservation{
				ID:              "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
				Name:            "zero-created-snap",
				VMID:            targetID,
				ObservedAt:      now,
				ObservationType: domain.ObservationObserved,
			},
		},
		{
			name: "zero observed_at",
			chk: domain.CheckpointObservation{
				ID:              "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
				Name:            "zero-observed-snap",
				VMID:            targetID,
				CreatedAt:       now,
				ObservationType: domain.ObservationObserved,
			},
		},
		{
			name: "non-observed type",
			chk: domain.CheckpointObservation{
				ID:              "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
				Name:            "bad-type-snap",
				VMID:            targetID,
				CreatedAt:       now,
				ObservedAt:      now,
				ObservationType: domain.ObservationInferred,
			},
		},
		{
			name: "mismatched VMID",
			chk: domain.CheckpointObservation{
				ID:              "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
				Name:            "other-vm-snap",
				VMID:            "99999999-9999-9999-9999-999999999999",
				CreatedAt:       now,
				ObservedAt:      now,
				ObservationType: domain.ObservationObserved,
			},
		},
		{
			name: "empty VMID",
			chk: domain.CheckpointObservation{
				ID:              "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
				Name:            "empty-vmid-snap",
				VMID:            "",
				CreatedAt:       now,
				ObservedAt:      now,
				ObservationType: domain.ObservationObserved,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := &mockBackend{
				listCheckpointsFn: func(_ context.Context, _ string) ([]domain.CheckpointObservation, error) {
					return []domain.CheckpointObservation{tc.chk}, nil
				},
				startMachineFn: func(_ context.Context, id string) (domain.MachineObservation, error) {
					return domain.MachineObservation{ID: id, State: domain.MachineStateRunning}, nil
				},
			}

			svc, _ := setupTestRecovery(t, backend)
			actor, _ := domain.NewActorContext("user:admin", "user:admin", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))

			req := app.MutationRequest{
				TargetID:       targetID,
				Actor:          actor,
				Reason:         "start test with malformed checkpoint",
				IdempotencyKey: "key-malformed-chk-" + tc.name,
				Timeout:        30 * time.Second,
			}

			_, _, err := svc.StartMachine(context.Background(), req)
			if err == nil {
				t.Fatalf("expected policy denial for unapproved start when checkpoint candidate is malformed/mismatched")
			}

			var deniedErr *app.PolicyDeniedError
			if !errors.As(err, &deniedErr) {
				t.Fatalf("expected PolicyDeniedError, got %v", err)
			}
		})
	}
}

func TestRecoveryService_ProviderSuccess_DirectorySyncFailure_ReturnsFinalizationError(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	snapID := "e4a523d4-6b99-4d62-a5e2-4752c0f20001"
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	backend := &mockBackend{
		listCheckpointsFn: func(_ context.Context, id string) ([]domain.CheckpointObservation, error) {
			return []domain.CheckpointObservation{
				{
					ID:              snapID,
					Name:            "baseline-snap",
					VMID:            id,
					CheckpointType:  "Standard",
					CreatedAt:       now.Add(-time.Hour),
					ObservedAt:      now,
					ObservationType: domain.ObservationObserved,
				},
			}, nil
		},
		startMachineFn: func(_ context.Context, id string) (domain.MachineObservation, error) {
			return domain.MachineObservation{ID: id, State: domain.MachineStateRunning}, nil
		},
	}

	dir := t.TempDir()
	leasesDir := dir + "/leases"
	auditDir := dir + "/audit"
	receiptsDir := dir + "/receipts"
	approvalsDir := dir + "/approvals"

	_ = os.MkdirAll(leasesDir, 0700)
	_ = os.MkdirAll(auditDir, 0700)
	_ = os.MkdirAll(receiptsDir, 0700)
	_ = os.MkdirAll(approvalsDir, 0700)

	syncErr := errors.New("simulated disk sync error on directory")

	leaseMgr := lease.NewManager(leasesDir, lease.WithClock(func() time.Time { return now }))
	auditStore := audit.NewStore(auditDir, audit.WithClock(func() time.Time { return now }))
	receiptStore := receipt.NewStore(receiptsDir, receipt.WithSyncDir(func(_ string) error {
		return syncErr
	}))
	approvalStore := approval.NewStore(approvalsDir)

	svc := app.NewRecoveryService(
		backend,
		leaseMgr,
		auditStore,
		receiptStore,
		approvalStore,
		app.WithRecoveryClock(func() time.Time { return now }),
	)

	actor, _ := domain.NewActorContext("user:admin", "user:admin", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))
	req := app.MutationRequest{
		TargetID:       targetID,
		Actor:          actor,
		Reason:         "start test with failing directory sync",
		IdempotencyKey: "key-sync-fail-finalization",
		Timeout:        30 * time.Second,
	}

	rcpt, _, err := svc.StartMachine(context.Background(), req)
	if err == nil {
		t.Fatalf("expected finalization error when directory sync fails on receipt save")
	}
	if !strings.Contains(err.Error(), "durable finalization failed") {
		t.Errorf("expected finalization failure error message, got %v", err)
	}
	if rcpt.Outcome.Status != domain.OutcomeSuccess {
		t.Errorf("expected provider success in receipt record despite finalization failure, got %s", rcpt.Outcome.Status)
	}
}
