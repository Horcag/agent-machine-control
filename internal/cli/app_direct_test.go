package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/cli"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestCLI_GlobalDirectAndStateDir(t *testing.T) {
	tempDir := t.TempDir()
	customStateDir := filepath.Join(tempDir, "custom-state")

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
				ID:              id,
				Name:            "win11-target",
				State:           domain.MachineStateRunning,
				Generation:      2,
				Version:         "10.0",
				ObservedAt:      time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC),
				ObservationType: domain.ObservationObserved,
			}, nil
		},
	}

	appInstance := setupTestApp(t, backend, nil)

	var stdout, stderr bytes.Buffer
	code := appInstance.Run([]string{
		"--state-dir", customStateDir,
		"--direct",
		"machine", "start", targetID,
		"--reason", "testing custom state dir",
		"--idempotency-key", "key-custom-state",
		"--json",
	}, &stdout, &stderr)

	if code != cli.ExitSuccess {
		t.Fatalf("expected ExitSuccess, got %d. stderr: %s", code, stderr.String())
	}
}

func TestCLI_DefaultRun_Direct_ExecutableMissing(t *testing.T) {
	// Target authority must reject before the unavailable Hyper-V executable is consulted.
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", "")
	defer func() { _ = os.Setenv("PATH", oldPath) }()

	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	stateDir := filepath.Join(t.TempDir(), "state")
	var stdout, stderr bytes.Buffer
	code := cli.Run([]string{
		"--state-dir", stateDir,
		"--direct", "checkpoint", "list", targetID,
	}, &stdout, &stderr)

	if code != cli.ExitConflict {
		t.Fatalf("expected ExitConflict (8) for the missing enrolled target, got %d. stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "no target is enrolled; enroll a local target first") {
		t.Errorf("expected sanitized target message on stderr, got %q", stderr.String())
	}
}

func TestCLI_DefaultRun_Direct_MutatingCommand(t *testing.T) {
	tempDir := t.TempDir()
	customStateDir := filepath.Join(tempDir, "custom_state")
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	var stdout, stderr bytes.Buffer
	// Test --state-dir <dir>
	code := cli.Run([]string{
		"--direct", "machine", "start", targetID,
		"--state-dir", customStateDir,
		"--reason", "test direct run",
		"--idempotency-key", "key-direct-run",
	}, &stdout, &stderr)

	if code != cli.ExitBackendUnavailable {
		t.Logf("direct run returned %d (stderr: %s)", code, stderr.String())
	}

	// Test --state-dir=<dir> with checkpoint
	stdout.Reset()
	stderr.Reset()
	code = cli.Run([]string{
		"--direct", "checkpoint", "create", targetID,
		"--state-dir=" + customStateDir,
		"--name", "test-chk",
		"--reason", "test direct run",
		"--idempotency-key", "key-direct-chk",
	}, &stdout, &stderr)

	if code != cli.ExitBackendUnavailable {
		t.Logf("direct checkpoint run returned %d (stderr: %s)", code, stderr.String())
	}

	// Test failed state-dir resolution
	stdout.Reset()
	stderr.Reset()
	t.Setenv("AMC_STATE_DIR", "/dev/null/impossible")
	code = cli.Run([]string{
		"--direct", "machine", "start", targetID,
		"--reason", "test invalid state dir",
		"--idempotency-key", "key-direct-err",
	}, &stdout, &stderr)

	if code != cli.ExitBackendUnavailable {
		t.Fatalf("expected ExitBackendUnavailable for invalid state dir, got %d", code)
	}
}

func TestCLI_AppOptions(t *testing.T) {
	appInstance := cli.NewApp(nil, cli.WithDirectMode(true))
	var stdout, stderr bytes.Buffer
	code := appInstance.Run([]string{"--version"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("expected ExitSuccess, got %d", code)
	}
}
