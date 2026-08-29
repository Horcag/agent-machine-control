package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/cli"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestCLI_MachineStart_RequiresDirectMode(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	backend := &mockBackend{}
	appInstance := setupTestApp(t, backend, nil)

	var stdout, stderr bytes.Buffer
	code := appInstance.Run([]string{
		"machine", "start", targetID,
		"--reason", "testing start",
		"--idempotency-key", "key-start-1",
	}, &stdout, &stderr)

	if code != cli.ExitBackendUnavailable {
		t.Fatalf("expected ExitBackendUnavailable when --direct is omitted, got %d", code)
	}
	if !strings.Contains(stderr.String(), "daemon is unavailable; run 'amcd run' or use '--direct'") {
		t.Errorf("unexpected stderr: %s", stderr.String())
	}
}

func TestCLI_MachineStart_WithRollback_Succeeds_HumanAndJSON(t *testing.T) {
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
		startMachineFn: func(_ context.Context, id string) (domain.MachineObservation, error) {
			return domain.MachineObservation{
				ID:                  id,
				Name:                "win11-target",
				State:               domain.MachineStateRunning,
				Generation:          2,
				Version:             "10.0",
				UptimeMs:            1000,
				CPUUsagePercent:     2,
				MemoryAssignedBytes: 4294967296,
				ObservedAt:          time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC),
				ObservationType:     domain.ObservationObserved,
			}, nil
		},
	}

	appInstance := setupTestApp(t, backend, nil)

	// Human Output
	var stdout, stderr bytes.Buffer
	code := appInstance.Run([]string{
		"--direct", "machine", "start", targetID,
		"--reason", "testing start command",
		"--idempotency-key", "key-start-human",
	}, &stdout, &stderr)

	if code != cli.ExitSuccess {
		t.Fatalf("expected ExitSuccess, got %d. stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Machine started successfully") {
		t.Errorf("expected success message: %s", stdout.String())
	}
	if !strings.Contains(stdout.String(), snapID) {
		t.Errorf("expected rollback ref in output: %s", stdout.String())
	}

	// JSON Output
	stdout.Reset()
	stderr.Reset()
	code = appInstance.Run([]string{
		"--direct", "machine", "start", targetID,
		"--reason", "testing start command json",
		"--idempotency-key", "key-start-json",
		"--json",
	}, &stdout, &stderr)

	if code != cli.ExitSuccess {
		t.Fatalf("expected ExitSuccess, got %d. stderr: %s", code, stderr.String())
	}

	var env cli.MachineMutationOutputEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("failed to parse JSON envelope: %v", err)
	}
	if env.SchemaVersion != "1" {
		t.Errorf("expected schema_version 1, got %s", env.SchemaVersion)
	}
	if env.Receipt.RollbackRef != snapID {
		t.Errorf("expected rollback_ref %s, got %s", snapID, env.Receipt.RollbackRef)
	}
	if env.Machine == nil || env.Machine.State != domain.MachineStateRunning {
		t.Errorf("expected machine running, got %v", env.Machine)
	}
}

func TestCLI_MachineStop_Modes(t *testing.T) {
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
		stopMachineFn: func(_ context.Context, id string, _ string) (domain.MachineObservation, error) {
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

	modes := []string{"shutdown", "save", "turn-off"}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := appInstance.Run([]string{
				"--direct", "machine", "stop", targetID,
				"--mode", mode,
				"--reason", "testing stop " + mode,
				"--idempotency-key", "key-stop-" + mode,
				"--json",
			}, &stdout, &stderr)

			if code != cli.ExitSuccess {
				t.Fatalf("expected ExitSuccess for mode %s, got %d. stderr: %s", mode, code, stderr.String())
			}

			var env cli.MachineMutationOutputEnvelope
			if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
				t.Fatalf("failed to parse JSON envelope: %v", err)
			}
			if env.Receipt.Outcome.Status != domain.OutcomeSuccess {
				t.Errorf("expected outcome success, got %s", env.Receipt.Outcome.Status)
			}
		})
	}
}

func TestCLI_MachineMutation_ValidationErrors(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	backend := &mockBackend{}
	appInstance := setupTestApp(t, backend, nil)

	tests := []struct {
		name string
		args []string
		code int
	}{
		{"start missing guid", []string{"--direct", "machine", "start"}, cli.ExitUsage},
		{"start invalid guid", []string{"--direct", "machine", "start", "not-a-guid", "--reason", "r", "--idempotency-key", "k"}, cli.ExitUsage},
		{"start missing reason", []string{"--direct", "machine", "start", targetID, "--idempotency-key", "k"}, cli.ExitUsage},
		{"start missing idemp key", []string{"--direct", "machine", "start", targetID, "--reason", "r"}, cli.ExitUsage},
		{"start invalid timeout", []string{"--direct", "machine", "start", targetID, "--reason", "r", "--idempotency-key", "k", "--timeout", "-1s"}, cli.ExitUsage},
		{"stop invalid mode", []string{"--direct", "machine", "stop", targetID, "--mode", "destroy", "--reason", "r", "--idempotency-key", "k"}, cli.ExitUsage},
		{"stop missing guid", []string{"--direct", "machine", "stop"}, cli.ExitUsage},
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

func TestCLI_MachineMutation_PrompterRejection(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	backend := &mockBackend{}
	prompter := &testPrompter{confirm: false} // operator denies
	appInstance := setupTestApp(t, backend, prompter)

	var stdout, stderr bytes.Buffer
	code := appInstance.Run([]string{
		"--direct", "machine", "start", targetID,
		"--reason", "testing start",
		"--idempotency-key", "key-start-reject",
	}, &stdout, &stderr)

	if code != cli.ExitDenied {
		t.Fatalf("expected ExitDenied on prompt rejection, got %d", code)
	}
}

func newMockBackendWithCheckpoint(snapID string) *mockBackend {
	return &mockBackend{
		listCheckpointsFn: func(_ context.Context, id string) ([]domain.CheckpointObservation, error) {
			return []domain.CheckpointObservation{
				{
					ID:              snapID,
					Name:            "baseline-snap",
					VMID:            id,
					CheckpointType:  "Standard",
					CreatedAt:       time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC),
					ObservedAt:      time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC),
					ObservationType: domain.ObservationObserved,
				},
			}, nil
		},
		stopMachineFn: func(_ context.Context, id string, _ string) (domain.MachineObservation, error) {
			return domain.MachineObservation{
				ID:    id,
				State: domain.MachineStateOff,
			}, nil
		},
	}
}

func TestCLI_MachineStop_ModesAndErrors(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	snapID := "e4a523d4-6b99-4d62-a5e2-4752c0f20001"
	backend := newMockBackendWithCheckpoint(snapID)
	appInstance := setupTestApp(t, backend, &testPrompter{confirm: true})

	var stdout, stderr bytes.Buffer

	// 1. Direct stop with mode=save
	code := appInstance.Run([]string{
		"--direct", "machine", "stop", targetID,
		"--mode", "save",
		"--reason", "testing save",
		"--idempotency-key", "key-stop-save-1",
	}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("expected ExitSuccess for stop mode=save, got %d; stderr: %s", code, stderr.String())
	}

	// 2. Direct stop with mode=turn-off
	stdout.Reset()
	stderr.Reset()
	code = appInstance.Run([]string{
		"--direct", "machine", "stop", targetID,
		"--mode", "turn-off",
		"--reason", "testing turn off",
		"--idempotency-key", "key-stop-off-1",
	}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("expected ExitSuccess for stop mode=turn-off, got %d; stderr: %s", code, stderr.String())
	}

	// 3. Invalid mode
	stderr.Reset()
	code = appInstance.Run([]string{
		"--direct", "machine", "stop", targetID,
		"--mode", "invalid-mode",
		"--reason", "testing invalid",
		"--idempotency-key", "key-stop-invalid-1",
	}, &stdout, &stderr)
	if code != cli.ExitUsage {
		t.Errorf("expected ExitUsage for invalid mode, got %d", code)
	}
}

func TestCLI_MachineStop_DirectJSON(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	snapID := "e4a523d4-6b99-4d62-a5e2-4752c0f20001"
	backend := newMockBackendWithCheckpoint(snapID)
	appInstance := setupTestApp(t, backend, &testPrompter{confirm: true})

	var stdout, stderr bytes.Buffer
	code := appInstance.Run([]string{
		"--direct", "machine", "stop", targetID,
		"--mode", "shutdown",
		"--reason", "testing direct stop json",
		"--idempotency-key", "key-stop-direct-json-1",
		"--json",
	}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("expected ExitSuccess for direct stop --json, got %d; stderr: %s", code, stderr.String())
	}
}
