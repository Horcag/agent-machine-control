package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/cli"
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
}

func (m *mockBackend) Doctor(ctx context.Context) (app.DoctorReport, error) {
	if m.doctorFn != nil {
		return m.doctorFn(ctx)
	}
	return app.DoctorReport{}, nil
}

func (m *mockBackend) ListMachines(ctx context.Context) ([]domain.MachineObservation, error) {
	if m.listMachinesFn != nil {
		return m.listMachinesFn(ctx)
	}
	return nil, nil
}

func (m *mockBackend) InspectMachine(ctx context.Context, id string) (domain.MachineObservation, error) {
	if m.inspectMachineFn != nil {
		return m.inspectMachineFn(ctx, id)
	}
	return domain.MachineObservation{}, nil
}

func (m *mockBackend) Capabilities(ctx context.Context, target string) (domain.CapabilitySet, error) {
	if m.capabilitiesFn != nil {
		return m.capabilitiesFn(ctx, target)
	}
	return domain.DirectMachineCapabilities(), nil
}

func (m *mockBackend) StartMachine(ctx context.Context, id string) (domain.MachineObservation, error) {
	if m.startMachineFn != nil {
		return m.startMachineFn(ctx, id)
	}
	return domain.MachineObservation{ID: id, State: domain.MachineStateRunning}, nil
}

func (m *mockBackend) StopMachine(ctx context.Context, id string, mode string) (domain.MachineObservation, error) {
	if m.stopMachineFn != nil {
		return m.stopMachineFn(ctx, id, mode)
	}
	return domain.MachineObservation{ID: id, State: domain.MachineStateOff}, nil
}

func (m *mockBackend) ListCheckpoints(ctx context.Context, id string) ([]domain.CheckpointObservation, error) {
	if m.listCheckpointsFn != nil {
		return m.listCheckpointsFn(ctx, id)
	}
	return nil, nil
}

func (m *mockBackend) CreateCheckpoint(ctx context.Context, id string, name string) (domain.CheckpointObservation, error) {
	if m.createCheckpointFn != nil {
		return m.createCheckpointFn(ctx, id, name)
	}
	return domain.CheckpointObservation{
		ID:              "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Name:            name,
		VMID:            id,
		CheckpointType:  "Standard",
		CreatedAt:       time.Now().UTC(),
		ObservedAt:      time.Now().UTC(),
		ObservationType: domain.ObservationObserved,
	}, nil
}

func (m *mockBackend) RestoreCheckpoint(ctx context.Context, id string, checkpointID string) (domain.MachineObservation, error) {
	if m.restoreCheckpointFn != nil {
		return m.restoreCheckpointFn(ctx, id, checkpointID)
	}
	return domain.MachineObservation{ID: id, State: domain.MachineStateRunning}, nil
}

type testPrompter struct {
	confirm bool
}

func (p *testPrompter) PromptConfirmation(_ string) bool {
	return p.confirm
}

func setupTestApp(t *testing.T, backend *mockBackend, prompter cli.Prompter) *cli.App {
	t.Helper()
	dir := t.TempDir()
	leasesDir := filepath.Join(dir, "leases")
	auditDir := filepath.Join(dir, "audit")
	receiptsDir := filepath.Join(dir, "receipts")
	approvalsDir := filepath.Join(dir, "approvals")

	_ = os.MkdirAll(leasesDir, 0700)
	_ = os.MkdirAll(auditDir, 0700)
	_ = os.MkdirAll(receiptsDir, 0700)
	_ = os.MkdirAll(approvalsDir, 0700)

	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	leaseMgr := lease.NewManager(leasesDir, lease.WithClock(func() time.Time { return now }))
	auditStore := audit.NewStore(auditDir, audit.WithClock(func() time.Time { return now }))
	receiptStore := receipt.NewStore(receiptsDir)
	approvalStore := approval.NewStore(approvalsDir)

	recoverySvc := app.NewRecoveryService(
		backend,
		leaseMgr,
		auditStore,
		receiptStore,
		approvalStore,
		app.WithRecoveryClock(func() time.Time { return now }),
	)
	discoverySvc := app.NewDiscoveryService(backend)

	actorCtx, _ := domain.NewActorContext("user:admin", "user:admin", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))

	return cli.NewApp(
		discoverySvc,
		cli.WithRecoveryService(recoverySvc),
		cli.WithActor(actorCtx),
		cli.WithPrompter(prompter),
		cli.WithClock(func() time.Time { return now }),
	)
}

func TestCLI_CheckpointList_Success_HumanAndJSON(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	snapID := "e4a523d4-6b99-4d62-a5e2-4752c0f20001"

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
	}

	appInstance := setupTestApp(t, backend, nil)

	// Human output
	var stdout, stderr bytes.Buffer
	code := appInstance.Run([]string{"checkpoint", "list", targetID}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("expected ExitSuccess, got %d. stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "baseline-snap") {
		t.Errorf("expected baseline-snap in human output: %s", stdout.String())
	}

	// JSON output
	stdout.Reset()
	stderr.Reset()
	code = appInstance.Run([]string{"checkpoint", "list", targetID, "--json"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("expected ExitSuccess, got %d. stderr: %s", code, stderr.String())
	}

	var env cli.CheckpointListOutputEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("failed to parse JSON envelope: %v", err)
	}
	if env.SchemaVersion != "1" {
		t.Errorf("expected schema_version 1, got %s", env.SchemaVersion)
	}
	if len(env.Checkpoints) != 1 || env.Checkpoints[0].ID != snapID {
		t.Errorf("unexpected checkpoints: %v", env.Checkpoints)
	}
}

func TestCLI_CheckpointCreate_RequiresDirectMode(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	backend := &mockBackend{}
	appInstance := setupTestApp(t, backend, nil)

	var stdout, stderr bytes.Buffer
	code := appInstance.Run([]string{
		"checkpoint", "create", targetID,
		"--name", "test-snap",
		"--reason", "testing",
		"--idempotency-key", "key-snap-1",
	}, &stdout, &stderr)

	if code != cli.ExitBackendUnavailable {
		t.Fatalf("expected ExitBackendUnavailable when --direct is omitted, got %d", code)
	}
	if !strings.Contains(stderr.String(), "daemon transport is not yet available; use '--direct'") {
		t.Errorf("unexpected stderr: %s", stderr.String())
	}
}

func TestCLI_CheckpointCreate_WithDirect_InteractiveConfirmation(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	backend := &mockBackend{}
	prompter := &testPrompter{confirm: true}
	appInstance := setupTestApp(t, backend, prompter)

	var stdout, stderr bytes.Buffer
	code := appInstance.Run([]string{
		"--direct", "checkpoint", "create", targetID,
		"--name", "snap-test",
		"--reason", "testing direct create",
		"--idempotency-key", "key-snap-create-1",
		"--json",
	}, &stdout, &stderr)

	if code != cli.ExitSuccess {
		t.Fatalf("expected ExitSuccess with direct and confirmed prompter, got %d. stderr: %s", code, stderr.String())
	}

	var env cli.CheckpointMutationOutputEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("failed to unmarshal JSON envelope: %v", err)
	}
	if env.Receipt.Outcome.Status != domain.OutcomeSuccess {
		t.Errorf("expected success status, got %s", env.Receipt.Outcome.Status)
	}
	if env.Checkpoint == nil || env.Checkpoint.Name != "snap-test" {
		t.Errorf("expected checkpoint snap-test, got %v", env.Checkpoint)
	}
}

func TestCLI_CheckpointRestore_RequiresDirectMode(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	snapID := "e4a523d4-6b99-4d62-a5e2-4752c0f20001"
	backend := &mockBackend{}
	appInstance := setupTestApp(t, backend, nil)

	var stdout, stderr bytes.Buffer
	code := appInstance.Run([]string{
		"checkpoint", "restore", targetID, snapID,
		"--reason", "testing",
		"--idempotency-key", "key-restore-1",
	}, &stdout, &stderr)

	if code != cli.ExitBackendUnavailable {
		t.Fatalf("expected ExitBackendUnavailable when --direct is omitted, got %d", code)
	}
	if !strings.Contains(stderr.String(), "daemon transport is not yet available; use '--direct'") {
		t.Errorf("unexpected stderr: %s", stderr.String())
	}
}

func TestCLI_CheckpointRestore_WithDirect_InteractiveConfirmation(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	snapID := "e4a523d4-6b99-4d62-a5e2-4752c0f20001"
	backend := &mockBackend{
		restoreCheckpointFn: func(_ context.Context, id string, _ string) (domain.MachineObservation, error) {
			return domain.MachineObservation{
				ID:              id,
				Name:            "win11-target",
				State:           domain.MachineStateOff,
				Generation:      2,
				Version:         "10.0",
				ObservedAt:      time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC),
				ObservationType: domain.ObservationObserved,
			}, nil
		},
	}
	prompter := &testPrompter{confirm: true}
	appInstance := setupTestApp(t, backend, prompter)

	// Human output
	var stdout, stderr bytes.Buffer
	code := appInstance.Run([]string{
		"--direct", "checkpoint", "restore", targetID, snapID,
		"--reason", "testing direct restore",
		"--idempotency-key", "key-restore-human",
	}, &stdout, &stderr)

	if code != cli.ExitSuccess {
		t.Fatalf("expected ExitSuccess, got %d. stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Checkpoint restored successfully") {
		t.Errorf("expected success message in stdout: %s", stdout.String())
	}

	// JSON output
	stdout.Reset()
	stderr.Reset()
	code = appInstance.Run([]string{
		"--direct", "checkpoint", "restore", targetID, snapID,
		"--reason", "testing direct restore json",
		"--idempotency-key", "key-restore-json",
		"--json",
	}, &stdout, &stderr)

	if code != cli.ExitSuccess {
		t.Fatalf("expected ExitSuccess, got %d. stderr: %s", code, stderr.String())
	}

	var env cli.MachineMutationOutputEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("failed to unmarshal JSON envelope: %v", err)
	}
	if env.Receipt.Outcome.Status != domain.OutcomeSuccess {
		t.Errorf("expected success status, got %s", env.Receipt.Outcome.Status)
	}
}

func TestCLI_Checkpoint_ValidationErrors(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	snapID := "e4a523d4-6b99-4d62-a5e2-4752c0f20001"
	backend := &mockBackend{}
	appInstance := setupTestApp(t, backend, nil)

	tests := []struct {
		name string
		args []string
		code int
	}{
		{"missing subcommand", []string{"checkpoint"}, cli.ExitUsage},
		{"unknown subcommand", []string{"checkpoint", "delete"}, cli.ExitUsage},
		{"list missing guid", []string{"checkpoint", "list"}, cli.ExitUsage},
		{"list invalid guid", []string{"checkpoint", "list", "invalid-guid"}, cli.ExitUsage},
		{"create missing name", []string{"--direct", "checkpoint", "create", targetID, "--reason", "r", "--idempotency-key", "k"}, cli.ExitUsage},
		{"create missing guid", []string{"--direct", "checkpoint", "create", "--name", "s", "--reason", "r", "--idempotency-key", "k"}, cli.ExitUsage},
		{"restore missing target", []string{"--direct", "checkpoint", "restore", "--reason", "r", "--idempotency-key", "k"}, cli.ExitUsage},
		{"restore missing snap", []string{"--direct", "checkpoint", "restore", targetID, "--reason", "r", "--idempotency-key", "k"}, cli.ExitUsage},
		{"restore invalid snap", []string{"--direct", "checkpoint", "restore", targetID, "not-a-guid", "--reason", "r", "--idempotency-key", "k"}, cli.ExitUsage},
		{"restore prompter reject", []string{"--direct", "checkpoint", "restore", targetID, snapID, "--reason", "r", "--idempotency-key", "k"}, cli.ExitDenied},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := appInstance.Run(tc.args, &stdout, &stderr)
			if code != tc.code {
				t.Errorf("expected code %d, got %d. stderr: %s", tc.code, code, stderr.String())
			}
		})
	}
}
