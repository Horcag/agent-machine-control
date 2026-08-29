package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/cli"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

func TestCLI_ReadOnlyDisasterRecovery_IndependentOfMutationState(t *testing.T) {
	// Set AMC_STATE_DIR to an uncreatable / invalid path
	impossibleStateDir := filepath.Join(t.TempDir(), "impossible", "nested", "path")
	// Make parent read-only to guarantee EnsureDirs / Resolve failure
	parent := filepath.Dir(filepath.Dir(impossibleStateDir))
	_ = os.MkdirAll(parent, 0500)
	defer func() { _ = os.Chmod(parent, 0700) }()

	t.Setenv(statedir.EnvStateDir, impossibleStateDir)

	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	snapID := "e4a523d4-6b99-4d62-a5e2-4752c0f20001"

	var providerCalls int
	backend := &mockBackend{
		doctorFn: func(_ context.Context) (app.DoctorReport, error) {
			return app.NewReadyReport(domain.ReadOnlyMachineCapabilities(), time.Now()), nil
		},
		listMachinesFn: func(_ context.Context) ([]domain.MachineObservation, error) {
			return []domain.MachineObservation{
				{ID: targetID, Name: "vm-1", State: domain.MachineStateRunning},
			}, nil
		},
		inspectMachineFn: func(_ context.Context, id string) (domain.MachineObservation, error) {
			return domain.MachineObservation{ID: id, Name: "vm-1", State: domain.MachineStateRunning}, nil
		},
		listCheckpointsFn: func(_ context.Context, id string) ([]domain.CheckpointObservation, error) {
			return []domain.CheckpointObservation{
				{ID: snapID, Name: "chk-1", VMID: id, CreatedAt: time.Now().UTC(), ObservedAt: time.Now().UTC(), ObservationType: domain.ObservationObserved},
			}, nil
		},
		startMachineFn: func(_ context.Context, _ string) (domain.MachineObservation, error) {
			providerCalls++
			return domain.MachineObservation{ID: targetID, State: domain.MachineStateRunning}, nil
		},
		stopMachineFn: func(_ context.Context, _ string, _ string) (domain.MachineObservation, error) {
			providerCalls++
			return domain.MachineObservation{ID: targetID, State: domain.MachineStateOff}, nil
		},
		createCheckpointFn: func(_ context.Context, id string, _ string) (domain.CheckpointObservation, error) {
			providerCalls++
			return domain.CheckpointObservation{ID: snapID, Name: "chk-1", VMID: id, CreatedAt: time.Now().UTC(), ObservedAt: time.Now().UTC(), ObservationType: domain.ObservationObserved}, nil
		},
		restoreCheckpointFn: func(_ context.Context, _ string, _ string) (domain.MachineObservation, error) {
			providerCalls++
			return domain.MachineObservation{ID: targetID, State: domain.MachineStateRunning}, nil
		},
	}

	readOnlySvc := app.NewDiscoveryService(backend)
	readOnlyRecoverySvc := app.NewRecoveryService(backend, nil, nil, nil, nil)
	appInstance := cli.NewApp(readOnlySvc, cli.WithRecoveryService(readOnlyRecoverySvc))

	// 1. amc doctor proceeds
	var stdout, stderr bytes.Buffer
	code := appInstance.Run([]string{"doctor"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("expected doctor to succeed, got %d. stderr: %s", code, stderr.String())
	}

	// 2. amc machine list proceeds
	stdout.Reset()
	stderr.Reset()
	code = appInstance.Run([]string{"machine", "list"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("expected machine list to succeed, got %d. stderr: %s", code, stderr.String())
	}

	// 3. amc machine inspect proceeds
	stdout.Reset()
	stderr.Reset()
	code = appInstance.Run([]string{"machine", "inspect", targetID}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("expected machine inspect to succeed, got %d. stderr: %s", code, stderr.String())
	}

	// 4. amc checkpoint list proceeds
	stdout.Reset()
	stderr.Reset()
	code = appInstance.Run([]string{"checkpoint", "list", targetID}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("expected checkpoint list to succeed, got %d. stderr: %s", code, stderr.String())
	}

	// 5. amc checkpoint list with --direct proceeds
	stdout.Reset()
	stderr.Reset()
	code = appInstance.Run([]string{"--direct", "checkpoint", "list", targetID}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("expected --direct checkpoint list to succeed, got %d. stderr: %s", code, stderr.String())
	}

	// 6. Mutating commands fail BEFORE provider execution when dependencies are missing or state resolution fails
	mutatingCommands := [][]string{
		{"--direct", "machine", "start", targetID, "--reason", "test", "--idempotency-key", "k1"},
		{"--direct", "machine", "stop", targetID, "--mode", "shutdown", "--reason", "test", "--idempotency-key", "k2"},
		{"--direct", "checkpoint", "create", targetID, "--name", "chk", "--reason", "test", "--idempotency-key", "k3"},
		{"--direct", "checkpoint", "restore", targetID, snapID, "--reason", "test", "--idempotency-key", "k4"},
	}

	for _, cmd := range mutatingCommands {
		stdout.Reset()
		stderr.Reset()
		code = appInstance.Run(cmd, &stdout, &stderr)
		if code == cli.ExitSuccess {
			t.Fatalf("expected mutating command %v to fail when storage dependencies are nil, got success", cmd)
		}
	}

	// Zero provider calls must have happened for all failing mutations
	if providerCalls != 0 {
		t.Fatalf("expected 0 provider calls, got %d", providerCalls)
	}
}
