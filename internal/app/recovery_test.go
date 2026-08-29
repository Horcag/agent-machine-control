package app_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

type mockBackend struct {
	doctorFn            func(ctx context.Context) (app.DoctorReport, error)
	listMachinesFn      func(ctx context.Context) ([]domain.MachineObservation, error)
	inspectMachineFn    func(ctx context.Context, id string) (domain.MachineObservation, error)
	capabilitiesFn      func(ctx context.Context, target string) (domain.CapabilitySet, error)
	startMachineFn      func(ctx context.Context, id string) (domain.MachineObservation, error)
	stopMachineFn       func(ctx context.Context, id string, mode string) (domain.MachineObservation, error)
	listCheckpointsFn   func(ctx context.Context, id string) ([]domain.CheckpointObservation, error)
	createCheckpointFn  func(ctx context.Context, id string, name string) (domain.CheckpointObservation, error)
	restoreCheckpointFn func(ctx context.Context, id string, checkpointID string) (domain.MachineObservation, error)
	calls               []string
}

func (m *mockBackend) Doctor(ctx context.Context) (app.DoctorReport, error) {
	m.calls = append(m.calls, "Doctor")
	if m.doctorFn != nil {
		return m.doctorFn(ctx)
	}
	return app.DoctorReport{}, nil
}

func (m *mockBackend) ListMachines(ctx context.Context) ([]domain.MachineObservation, error) {
	m.calls = append(m.calls, "ListMachines")
	if m.listMachinesFn != nil {
		return m.listMachinesFn(ctx)
	}
	return nil, nil
}

func (m *mockBackend) InspectMachine(ctx context.Context, id string) (domain.MachineObservation, error) {
	m.calls = append(m.calls, "InspectMachine:"+id)
	if m.inspectMachineFn != nil {
		return m.inspectMachineFn(ctx, id)
	}
	return domain.MachineObservation{}, nil
}

func (m *mockBackend) Capabilities(ctx context.Context, target string) (domain.CapabilitySet, error) {
	m.calls = append(m.calls, "Capabilities:"+target)
	if m.capabilitiesFn != nil {
		return m.capabilitiesFn(ctx, target)
	}
	return domain.DirectMachineCapabilities(), nil
}

func (m *mockBackend) StartMachine(ctx context.Context, id string) (domain.MachineObservation, error) {
	m.calls = append(m.calls, "StartMachine:"+id)
	if m.startMachineFn != nil {
		return m.startMachineFn(ctx, id)
	}
	return domain.MachineObservation{ID: id, State: domain.MachineStateRunning}, nil
}

func (m *mockBackend) StopMachine(ctx context.Context, id string, mode string) (domain.MachineObservation, error) {
	m.calls = append(m.calls, "StopMachine:"+id+":"+mode)
	if m.stopMachineFn != nil {
		return m.stopMachineFn(ctx, id, mode)
	}
	return domain.MachineObservation{ID: id, State: domain.MachineStateOff}, nil
}

func (m *mockBackend) ListCheckpoints(ctx context.Context, id string) ([]domain.CheckpointObservation, error) {
	m.calls = append(m.calls, "ListCheckpoints:"+id)
	if m.listCheckpointsFn != nil {
		return m.listCheckpointsFn(ctx, id)
	}
	return nil, nil
}

func (m *mockBackend) CreateCheckpoint(ctx context.Context, id string, name string) (domain.CheckpointObservation, error) {
	m.calls = append(m.calls, "CreateCheckpoint:"+id+":"+name)
	if m.createCheckpointFn != nil {
		return m.createCheckpointFn(ctx, id, name)
	}
	return domain.CheckpointObservation{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20099", Name: name, VMID: id}, nil
}

func (m *mockBackend) RestoreCheckpoint(ctx context.Context, id string, checkpointID string) (domain.MachineObservation, error) {
	m.calls = append(m.calls, "RestoreCheckpoint:"+id+":"+checkpointID)
	if m.restoreCheckpointFn != nil {
		return m.restoreCheckpointFn(ctx, id, checkpointID)
	}
	return domain.MachineObservation{ID: id, State: domain.MachineStateRunning}, nil
}

func setupTestRecovery(t *testing.T, backend app.Backend) (*app.RecoveryService, string) {
	t.Helper()
	dir := t.TempDir()
	leasesDir := dir + "/leases"
	auditDir := dir + "/audit"
	receiptsDir := dir + "/receipts"
	approvalsDir := dir + "/approvals"

	_ = os.MkdirAll(leasesDir, 0700)
	_ = os.MkdirAll(auditDir, 0700)
	_ = os.MkdirAll(receiptsDir, 0700)
	_ = os.MkdirAll(approvalsDir, 0700)

	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	leaseMgr := lease.NewManager(leasesDir, lease.WithClock(func() time.Time { return now }))
	auditStore := audit.NewStore(auditDir, audit.WithClock(func() time.Time { return now }))
	receiptStore := receipt.NewStore(receiptsDir)
	approvalStore := approval.NewStore(approvalsDir)

	svc := app.NewRecoveryService(
		backend,
		leaseMgr,
		auditStore,
		receiptStore,
		approvalStore,
		app.WithRecoveryClock(func() time.Time { return now }),
	)
	return svc, dir
}

func TestRecoveryService_StartMachine_WithRollbackCheckpoint_SucceedsAsReversible(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	snapID := "c4a523d4-6b99-4d62-a5e2-4752c0f20002"

	backend := &mockBackend{
		listCheckpointsFn: func(_ context.Context, id string) ([]domain.CheckpointObservation, error) {
			return []domain.CheckpointObservation{
				{ID: snapID, Name: "healthy-snap", VMID: id, CreatedAt: time.Now(), ObservedAt: time.Now(), ObservationType: domain.ObservationObserved},
			}, nil
		},
	}

	svc, _ := setupTestRecovery(t, backend)

	actor, _ := domain.NewActorContext("user:admin", "user:admin", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))
	req := app.MutationRequest{
		TargetID:       targetID,
		Actor:          actor,
		Reason:         "operator start request",
		IdempotencyKey: "key-start-1",
		Timeout:        30 * time.Second,
	}

	rcpt, obs, err := svc.StartMachine(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected StartMachine error: %v", err)
	}

	if rcpt.Class != domain.ClassReversibleMutation {
		t.Errorf("expected ClassReversibleMutation, got %s", rcpt.Class)
	}
	if rcpt.RollbackRef != snapID {
		t.Errorf("expected RollbackRef %s, got %s", snapID, rcpt.RollbackRef)
	}
	if obs.State != domain.MachineStateRunning {
		t.Errorf("expected Running state, got %s", obs.State)
	}
}

func TestRecoveryService_StartMachine_WithoutRollback_ReclassifiesToDestructive_DeniedWithoutApproval(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	backend := &mockBackend{
		listCheckpointsFn: func(_ context.Context, _ string) ([]domain.CheckpointObservation, error) {
			return []domain.CheckpointObservation{}, nil // No checkpoints!
		},
	}

	svc, _ := setupTestRecovery(t, backend)

	actor, _ := domain.NewActorContext("user:admin", "user:admin", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))
	req := app.MutationRequest{
		TargetID:       targetID,
		Actor:          actor,
		Reason:         "operator start request",
		IdempotencyKey: "key-start-no-snap",
		Timeout:        30 * time.Second,
		// No approval supplied
	}

	_, _, err := svc.StartMachine(context.Background(), req)
	if err == nil {
		t.Fatalf("expected policy denial for unapproved destructive start without rollback")
	}

	var deniedErr *app.PolicyDeniedError
	if !errors.As(err, &deniedErr) {
		t.Fatalf("expected PolicyDeniedError, got %v", err)
	}
}

func TestRecoveryService_IdempotentRetry_ReturnsCachedReceipt_ZeroProviderCalls(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	snapID := "c4a523d4-6b99-4d62-a5e2-4752c0f20002"

	var providerCalls int
	backend := &mockBackend{
		listCheckpointsFn: func(_ context.Context, id string) ([]domain.CheckpointObservation, error) {
			return []domain.CheckpointObservation{
				{ID: snapID, Name: "healthy-snap", VMID: id, CreatedAt: time.Now(), ObservedAt: time.Now(), ObservationType: domain.ObservationObserved},
			}, nil
		},
		startMachineFn: func(_ context.Context, id string) (domain.MachineObservation, error) {
			providerCalls++
			return domain.MachineObservation{ID: id, State: domain.MachineStateRunning}, nil
		},
	}

	svc, _ := setupTestRecovery(t, backend)

	actor, _ := domain.NewActorContext("user:admin", "user:admin", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))
	req := app.MutationRequest{
		TargetID:       targetID,
		Actor:          actor,
		Reason:         "first invocation",
		IdempotencyKey: "idemp-key-repeat",
		Timeout:        30 * time.Second,
	}

	// First call
	rcpt1, _, err := svc.StartMachine(context.Background(), req)
	if err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if providerCalls != 1 {
		t.Fatalf("expected 1 provider call, got %d", providerCalls)
	}

	// Second identical call
	rcpt2, _, err := svc.StartMachine(context.Background(), req)
	if err != nil {
		t.Fatalf("retry call failed: %v", err)
	}
	if providerCalls != 1 {
		t.Fatalf("expected ZERO additional provider calls on retry, got %d", providerCalls)
	}
	if rcpt2.ReceiptID != rcpt1.ReceiptID {
		t.Errorf("expected identical cached receipt ID, got %s vs %s", rcpt1.ReceiptID, rcpt2.ReceiptID)
	}
}

func TestRecoveryService_UnwritableAudit_FailsClosed_ZeroProviderCalls(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	var providerCalls int
	backend := &mockBackend{
		startMachineFn: func(_ context.Context, id string) (domain.MachineObservation, error) {
			providerCalls++
			return domain.MachineObservation{ID: id, State: domain.MachineStateRunning}, nil
		},
	}

	svc, dir := setupTestRecovery(t, backend)

	// Make audit directory read-only
	auditDir := dir + "/audit"
	_ = os.Chmod(auditDir, 0500)
	defer func() { _ = os.Chmod(auditDir, 0700) }()

	actor, _ := domain.NewActorContext("user:admin", "user:admin", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))
	req := app.MutationRequest{
		TargetID:       targetID,
		Actor:          actor,
		Reason:         "test unwritable",
		IdempotencyKey: "key-unwritable",
		Timeout:        30 * time.Second,
	}

	_, _, err := svc.StartMachine(context.Background(), req)
	if err == nil || !errors.Is(err, app.ErrAuditUnavailable) {
		t.Fatalf("expected ErrAuditUnavailable, got %v", err)
	}
	if providerCalls != 0 {
		t.Fatalf("expected 0 provider calls when audit is unwritable, got %d", providerCalls)
	}
}

func TestRecoveryService_StopMachine_TurnOff_RequiresApproval(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	backend := &mockBackend{}
	svc, _ := setupTestRecovery(t, backend)

	actor, _ := domain.NewActorContext("user:admin", "user:admin", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))
	req := app.MutationRequest{
		TargetID:       targetID,
		Actor:          actor,
		Reason:         "hard turn off",
		IdempotencyKey: "key-stop-turnoff",
		Timeout:        30 * time.Second,
	}

	// Without approval: denied
	_, _, err := svc.StopMachine(context.Background(), req, "turn-off")
	if err == nil {
		t.Fatalf("expected policy denial for turn-off without approval")
	}

	// With matching approval: allowed
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	op := domain.Operation{
		Kind:                "machine.stop",
		Target:              domain.MachineRef(targetID),
		Actor:               actor,
		Reason:              "hard turn off",
		Deadline:            now.Add(30 * time.Second),
		IdempotencyKey:      "key-stop-turnoff",
		RequiredCapability:  domain.CapabilityMachineStop,
		RequiredScopes:      []string{"machine:write"},
		Classification:      domain.ClassDestructivePrivileged,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters:          map[string]any{"mode": "turn-off"},
	}
	fp, _ := op.Fingerprint()

	appr := domain.Approval{
		ID:              "app-turnoff-1",
		Actor:           actor.EffectiveActor,
		Target:          domain.MachineRef(targetID),
		AuthorizedClass: domain.ClassDestructivePrivileged,
		Fingerprint:     fp,
		IdempotencyKey:  "key-stop-turnoff",
		IssuedAt:        now,
		ExpiresAt:       now.Add(time.Hour),
	}
	req.Deadline = op.Deadline
	req.Approval = &appr

	rcpt, obs, err := svc.StopMachine(context.Background(), req, "turn-off")
	if err != nil {
		t.Fatalf("unexpected error with valid approval: %v", err)
	}
	if rcpt.Class != domain.ClassDestructivePrivileged {
		t.Errorf("expected ClassDestructivePrivileged, got %s", rcpt.Class)
	}
	if obs.State != domain.MachineStateOff {
		t.Errorf("expected Off state, got %s", obs.State)
	}
}

func TestRecoveryService_DeterministicRollbackSelection(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	chkOld := domain.CheckpointObservation{
		ID:              "11111111-1111-1111-1111-111111111111",
		Name:            "chk-old",
		VMID:            targetID,
		CreatedAt:       now.Add(-2 * time.Hour),
		ObservedAt:      now,
		ObservationType: domain.ObservationObserved,
	}
	chkTieB := domain.CheckpointObservation{
		ID:              "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
		Name:            "chk-tie-b",
		VMID:            targetID,
		CreatedAt:       now.Add(-1 * time.Hour),
		ObservedAt:      now,
		ObservationType: domain.ObservationObserved,
	}
	chkTieA := domain.CheckpointObservation{
		ID:              "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		Name:            "chk-tie-a",
		VMID:            targetID,
		CreatedAt:       now.Add(-1 * time.Hour),
		ObservedAt:      now,
		ObservationType: domain.ObservationObserved,
	}

	backend := &mockBackend{
		listCheckpointsFn: func(_ context.Context, _ string) ([]domain.CheckpointObservation, error) {
			return []domain.CheckpointObservation{chkOld, chkTieB, chkTieA}, nil
		},
		startMachineFn: func(_ context.Context, _ string) (domain.MachineObservation, error) {
			return domain.MachineObservation{ID: targetID, State: domain.MachineStateRunning}, nil
		},
	}

	svc, _ := setupTestRecovery(t, backend)
	actor, _ := domain.NewActorContext("user:admin", "user:admin", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))

	req := app.MutationRequest{
		TargetID:       targetID,
		Actor:          actor,
		Reason:         "start testing rollback selection",
		IdempotencyKey: "key-select-rollback",
		Timeout:        30 * time.Second,
	}

	rcpt, _, err := svc.StartMachine(context.Background(), req)
	if err != nil {
		t.Fatalf("StartMachine failed: %v", err)
	}

	// Must have chosen chkTieA (newest CreatedAt and lexicographically first ID)
	if rcpt.RollbackRef != chkTieA.ID {
		t.Fatalf("expected rollback ref %s, got %s", chkTieA.ID, rcpt.RollbackRef)
	}
	if rcpt.Class != domain.ClassReversibleMutation {
		t.Fatalf("expected ClassReversibleMutation with verified rollback, got %s", rcpt.Class)
	}
}

func TestRecoveryService_NilDependencies_FailsClosed(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	backend := &mockBackend{}

	// nil leaseManager
	svc := app.NewRecoveryService(backend, nil, nil, nil, nil, app.WithRecoveryClock(func() time.Time { return now }))
	actor, _ := domain.NewActorContext("user:admin", "user:admin", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))

	req := app.MutationRequest{
		TargetID:       targetID,
		Actor:          actor,
		Reason:         "start test",
		IdempotencyKey: "key-nil-deps",
		Timeout:        30 * time.Second,
	}

	_, _, err := svc.StartMachine(context.Background(), req)
	if err == nil {
		t.Fatalf("expected error when storage dependencies are nil")
	}
}

func TestRecoveryService_CapabilitiesError_FailsClosed(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	backend := &mockBackend{
		capabilitiesFn: func(_ context.Context, _ string) (domain.CapabilitySet, error) {
			return domain.CapabilitySet{}, errors.New("hypervisor WMI offline")
		},
	}

	svc, _ := setupTestRecovery(t, backend)
	actor, _ := domain.NewActorContext("user:admin", "user:admin", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))

	req := app.MutationRequest{
		TargetID:       targetID,
		Actor:          actor,
		Reason:         "start test",
		IdempotencyKey: "key-caps-err",
		Timeout:        30 * time.Second,
	}

	_, _, err := svc.StartMachine(context.Background(), req)
	if err == nil {
		t.Fatalf("expected error when Capabilities returns failure")
	}
}
