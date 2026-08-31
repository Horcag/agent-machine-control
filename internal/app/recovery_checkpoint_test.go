package app_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestRecoveryService_CreateCheckpoint_RequiresApproval(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	backend := &mockBackend{}
	svc, dir := setupTestRecovery(t, backend)

	actor, _ := domain.NewActorContext("user:admin", "user:admin", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))
	req := app.MutationRequest{
		TargetID:       targetID,
		Actor:          actor,
		Reason:         "checkpoint create",
		IdempotencyKey: "key-snap-create",
		Timeout:        30 * time.Second,
	}

	// Without approval
	_, _, err := svc.CreateCheckpoint(context.Background(), req, "my-checkpoint")
	if err == nil {
		t.Fatalf("expected policy denial for create checkpoint without approval")
	}

	// With matching approval
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	op := domain.Operation{
		Kind:                "checkpoint.create",
		Target:              domain.MachineRef(targetID),
		Actor:               actor,
		Reason:              "checkpoint create",
		Deadline:            now.Add(30 * time.Second),
		IdempotencyKey:      "key-snap-create-2",
		RequiredCapability:  domain.CapabilityCheckpointCreate,
		RequiredScopes:      []string{"machine:write"},
		Classification:      domain.ClassDestructivePrivileged,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters:          map[string]any{"name": "my-checkpoint"},
	}
	fp, _ := op.Fingerprint()

	appr := domain.Approval{
		ID:              "app-create-1",
		Actor:           actor.EffectiveActor,
		Target:          domain.MachineRef(targetID),
		AuthorizedClass: domain.ClassDestructivePrivileged,
		Fingerprint:     fp,
		IdempotencyKey:  "key-snap-create-2",
		IssuedAt:        now,
		ExpiresAt:       now.Add(time.Hour),
	}
	req.Deadline = op.Deadline
	req.Approval = &appr
	req.IdempotencyKey = "key-snap-create-2"
	if err := approval.NewStore(filepath.Join(dir, "approvals")).Issue(appr); err != nil {
		t.Fatal(err)
	}

	rcpt, snap, err := svc.CreateCheckpoint(context.Background(), req, "my-checkpoint")
	if err != nil {
		t.Fatalf("unexpected error with valid approval: %v", err)
	}
	if snap.Name != "my-checkpoint" {
		t.Errorf("expected checkpoint name my-checkpoint, got %s", snap.Name)
	}
	if rcpt.Outcome.Status != domain.OutcomeSuccess {
		t.Errorf("expected success outcome, got %s", rcpt.Outcome.Status)
	}
}

func TestRecoveryService_RestoreCheckpoint_RequiresApproval(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	snapID := "c4a523d4-6b99-4d62-a5e2-4752c0f20002"
	backend := &mockBackend{}
	svc, dir := setupTestRecovery(t, backend)

	actor, _ := domain.NewActorContext("user:admin", "user:admin", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))
	req := app.MutationRequest{
		TargetID:       targetID,
		Actor:          actor,
		Reason:         "checkpoint restore",
		IdempotencyKey: "key-snap-restore",
		Timeout:        30 * time.Second,
	}

	// Without approval
	_, _, err := svc.RestoreCheckpoint(context.Background(), req, snapID)
	if err == nil {
		t.Fatalf("expected policy denial for restore checkpoint without approval")
	}

	// With matching approval
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	op := domain.Operation{
		Kind:                "checkpoint.restore",
		Target:              domain.MachineRef(targetID),
		Actor:               actor,
		Reason:              "checkpoint restore",
		Deadline:            now.Add(30 * time.Second),
		IdempotencyKey:      "key-snap-restore-2",
		RequiredCapability:  domain.CapabilityCheckpointRestore,
		RequiredScopes:      []string{"machine:write"},
		Classification:      domain.ClassDestructivePrivileged,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters:          map[string]any{"checkpoint_id": snapID},
	}
	fp, _ := op.Fingerprint()

	appr := domain.Approval{
		ID:              "app-restore-1",
		Actor:           actor.EffectiveActor,
		Target:          domain.MachineRef(targetID),
		AuthorizedClass: domain.ClassDestructivePrivileged,
		Fingerprint:     fp,
		IdempotencyKey:  "key-snap-restore-2",
		IssuedAt:        now,
		ExpiresAt:       now.Add(time.Hour),
	}
	req.Deadline = op.Deadline
	req.Approval = &appr
	req.IdempotencyKey = "key-snap-restore-2"
	if err := approval.NewStore(filepath.Join(dir, "approvals")).Issue(appr); err != nil {
		t.Fatal(err)
	}

	rcpt, obs, err := svc.RestoreCheckpoint(context.Background(), req, snapID)
	if err != nil {
		t.Fatalf("unexpected error with valid approval: %v", err)
	}
	if obs.ID != targetID {
		t.Errorf("expected target ID %s, got %s", targetID, obs.ID)
	}
	if rcpt.Outcome.Status != domain.OutcomeSuccess {
		t.Errorf("expected success outcome, got %s", rcpt.Outcome.Status)
	}
}

func TestRecoveryService_ListCheckpoints_Direct(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	snapID := "c4a523d4-6b99-4d62-a5e2-4752c0f20002"
	backend := &mockBackend{
		listCheckpointsFn: func(_ context.Context, id string) ([]domain.CheckpointObservation, error) {
			return []domain.CheckpointObservation{
				{ID: snapID, Name: "snap-1", VMID: id, CreatedAt: time.Now(), ObservedAt: time.Now(), ObservationType: domain.ObservationObserved},
			}, nil
		},
	}
	svc, _ := setupTestRecovery(t, backend)

	snaps, err := svc.ListCheckpoints(context.Background(), targetID)
	if err != nil {
		t.Fatalf("ListCheckpoints failed: %v", err)
	}
	if len(snaps) != 1 || snaps[0].ID != snapID {
		t.Errorf("unexpected checkpoints: %v", snaps)
	}
}
