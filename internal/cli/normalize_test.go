package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/cli"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

type normalizeTestCase struct {
	name        string
	args        []string
	wantDirect  bool
	wantJSON    bool
	wantState   string
	wantCmdArgs []string
	wantErr     bool
}

func assertNormalized(t *testing.T, norm cli.NormalizedCLI, tc normalizeTestCase) {
	t.Helper()
	if norm.Direct != tc.wantDirect {
		t.Errorf("Direct = %v, want %v", norm.Direct, tc.wantDirect)
	}
	if norm.JSON != tc.wantJSON {
		t.Errorf("JSON = %v, want %v", norm.JSON, tc.wantJSON)
	}
	if norm.StateDir != tc.wantState {
		t.Errorf("StateDir = %q, want %q", norm.StateDir, tc.wantState)
	}
	if len(norm.CommandArgs) != len(tc.wantCmdArgs) {
		t.Fatalf("CommandArgs len = %d (%v), want %d (%v)", len(norm.CommandArgs), norm.CommandArgs, len(tc.wantCmdArgs), tc.wantCmdArgs)
	}
	for i := range norm.CommandArgs {
		if norm.CommandArgs[i] != tc.wantCmdArgs[i] {
			t.Errorf("CommandArgs[%d] = %q, want %q", i, norm.CommandArgs[i], tc.wantCmdArgs[i])
		}
	}
}

func TestNormalizeGlobalFlags_TableDriven(t *testing.T) {
	cases := []normalizeTestCase{
		{
			name:        "empty args",
			args:        []string{},
			wantCmdArgs: nil,
		},
		{
			name:        "leading json doctor",
			args:        []string{"--json", "doctor"},
			wantJSON:    true,
			wantCmdArgs: []string{"doctor"},
		},
		{
			name:        "trailing json doctor",
			args:        []string{"doctor", "--json"},
			wantJSON:    true,
			wantCmdArgs: []string{"doctor"},
		},
		{
			name:        "intermediate json machine list",
			args:        []string{"machine", "--json", "list"},
			wantJSON:    true,
			wantCmdArgs: []string{"machine", "list"},
		},
		{
			name:        "leading direct and json",
			args:        []string{"--direct", "--json", "machine", "start", "guid-1"},
			wantDirect:  true,
			wantJSON:    true,
			wantCmdArgs: []string{"machine", "start", "guid-1"},
		},
		{
			name:        "trailing direct and json with state dir",
			args:        []string{"machine", "start", "guid-1", "--state-dir", "/tmp/state", "--direct", "--json"},
			wantDirect:  true,
			wantJSON:    true,
			wantState:   "/tmp/state",
			wantCmdArgs: []string{"machine", "start", "guid-1"},
		},
		{
			name:        "state dir equals syntax",
			args:        []string{"--state-dir=/custom/dir", "--direct", "doctor"},
			wantDirect:  true,
			wantState:   "/custom/dir",
			wantCmdArgs: []string{"doctor"},
		},
		{
			name:        "single dash variants",
			args:        []string{"-direct", "-json", "-state-dir", "/my/dir", "machine", "list"},
			wantDirect:  true,
			wantJSON:    true,
			wantState:   "/my/dir",
			wantCmdArgs: []string{"machine", "list"},
		},
		{
			name:        "single dash state dir equals",
			args:        []string{"-state-dir=/my/dir", "doctor"},
			wantState:   "/my/dir",
			wantCmdArgs: []string{"doctor"},
		},
		{
			name:    "missing state dir value at end",
			args:    []string{"--state-dir"},
			wantErr: true,
		},
		{
			name:    "missing state dir value with empty equals",
			args:    []string{"--state-dir="},
			wantErr: true,
		},
		{
			name:    "missing single dash state dir value with empty equals",
			args:    []string{"-state-dir="},
			wantErr: true,
		},
		{
			name:    "missing state dir value trailing after command",
			args:    []string{"machine", "list", "--state-dir"},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			norm, err := cli.NormalizeGlobalFlags(tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("NormalizeGlobalFlags error = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr {
				assertNormalized(t, norm, tc)
			}
		})
	}
}

func TestCLI_GlobalFlagPlacements_TableDriven(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	snapID := "e4a523d4-6b99-4d62-a5e2-4752c0f20001"
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	backend := &mockBackend{
		doctorFn: func(_ context.Context) (app.DoctorReport, error) {
			return app.NewReadyReport(domain.DirectMachineCapabilities(), now), nil
		},
		listMachinesFn: func(_ context.Context) ([]domain.MachineObservation, error) {
			return []domain.MachineObservation{
				{ID: targetID, Name: "win11-test", State: domain.MachineStateRunning, ObservedAt: now, ObservationType: domain.ObservationObserved},
			}, nil
		},
		inspectMachineFn: func(_ context.Context, id string) (domain.MachineObservation, error) {
			return domain.MachineObservation{ID: id, Name: "win11-test", State: domain.MachineStateRunning, ObservedAt: now, ObservationType: domain.ObservationObserved}, nil
		},
		listCheckpointsFn: func(_ context.Context, id string) ([]domain.CheckpointObservation, error) {
			return []domain.CheckpointObservation{
				{ID: snapID, Name: "baseline-snap", VMID: id, CheckpointType: "Standard", CreatedAt: now.Add(-time.Hour), ObservedAt: now, ObservationType: domain.ObservationObserved},
			}, nil
		},
		startMachineFn: func(_ context.Context, id string) (domain.MachineObservation, error) {
			return domain.MachineObservation{ID: id, Name: "win11-test", State: domain.MachineStateRunning, ObservedAt: now, ObservationType: domain.ObservationObserved}, nil
		},
		stopMachineFn: func(_ context.Context, id, _ string) (domain.MachineObservation, error) {
			return domain.MachineObservation{ID: id, Name: "win11-test", State: domain.MachineStateOff, ObservedAt: now, ObservationType: domain.ObservationObserved}, nil
		},
		createCheckpointFn: func(_ context.Context, id, name string) (domain.CheckpointObservation, error) {
			return domain.CheckpointObservation{ID: snapID, Name: name, VMID: id, CheckpointType: "Standard", CreatedAt: now, ObservedAt: now, ObservationType: domain.ObservationObserved}, nil
		},
		restoreCheckpointFn: func(_ context.Context, id, _ string) (domain.MachineObservation, error) {
			return domain.MachineObservation{ID: id, Name: "win11-test", State: domain.MachineStateRunning, ObservedAt: now, ObservationType: domain.ObservationObserved}, nil
		},
	}

	appInstance := setupTestApp(t, backend, &testPrompter{confirm: true})
	customDir := filepath.Join(t.TempDir(), "custom-state")

	testCases := []struct {
		name     string
		args     []string
		wantJSON bool
	}{
		// Doctor placements
		{name: "doctor leading json", args: []string{"--json", "doctor"}, wantJSON: true},
		{name: "doctor trailing json", args: []string{"doctor", "--json"}, wantJSON: true},
		{name: "doctor single dash json", args: []string{"-json", "doctor"}, wantJSON: true},

		// Machine list placements
		{name: "machine list leading json", args: []string{"--json", "machine", "list"}, wantJSON: true},
		{name: "machine list intermediate json", args: []string{"machine", "--json", "list"}, wantJSON: true},
		{name: "machine list trailing json", args: []string{"machine", "list", "--json"}, wantJSON: true},
		{name: "machine list with state dir and json", args: []string{"--state-dir", customDir, "machine", "list", "--json"}, wantJSON: true},

		// Machine inspect placements
		{name: "machine inspect leading json", args: []string{"--json", "machine", "inspect", targetID}, wantJSON: true},
		{name: "machine inspect intermediate json", args: []string{"machine", "--json", "inspect", targetID}, wantJSON: true},
		{name: "machine inspect trailing json", args: []string{"machine", "inspect", targetID, "--json"}, wantJSON: true},

		// Machine start placements (requires direct)
		{name: "machine start leading direct and json", args: []string{"--direct", "--json", "machine", "start", targetID, "--reason", "test start", "--idempotency-key", "k-start-1"}, wantJSON: true},
		{name: "machine start intermediate direct trailing json", args: []string{"machine", "--direct", "start", targetID, "--reason", "test start", "--idempotency-key", "k-start-2", "--json"}, wantJSON: true},
		{name: "machine start trailing direct and json", args: []string{"machine", "start", targetID, "--reason", "test start", "--idempotency-key", "k-start-3", "--direct", "--json"}, wantJSON: true},
		{name: "machine start with state dir equals", args: []string{"--state-dir=" + customDir, "--direct", "--json", "machine", "start", targetID, "--reason", "test start", "--idempotency-key", "k-start-4"}, wantJSON: true},

		// Machine stop placements
		{name: "machine stop leading direct and json", args: []string{"--direct", "--json", "machine", "stop", targetID, "--mode", "shutdown", "--reason", "test stop", "--idempotency-key", "k-stop-1"}, wantJSON: true},
		{name: "machine stop intermediate direct trailing json", args: []string{"machine", "--direct", "stop", targetID, "--mode", "shutdown", "--reason", "test stop", "--idempotency-key", "k-stop-2", "--json"}, wantJSON: true},

		// Checkpoint list placements
		{name: "checkpoint list leading json", args: []string{"--json", "checkpoint", "list", targetID}, wantJSON: true},
		{name: "checkpoint list intermediate json", args: []string{"checkpoint", "--json", "list", targetID}, wantJSON: true},
		{name: "checkpoint list trailing json", args: []string{"checkpoint", "list", targetID, "--json"}, wantJSON: true},

		// Checkpoint create placements
		{name: "checkpoint create leading direct and json", args: []string{"--direct", "--json", "checkpoint", "create", targetID, "--name", "chk-1", "--reason", "test create", "--idempotency-key", "k-chk-1"}, wantJSON: true},
		{name: "checkpoint create trailing direct and json", args: []string{"checkpoint", "create", targetID, "--name", "chk-2", "--reason", "test create", "--idempotency-key", "k-chk-2", "--direct", "--json"}, wantJSON: true},

		// Checkpoint restore placements
		{name: "checkpoint restore leading direct and json", args: []string{"--direct", "--json", "checkpoint", "restore", targetID, snapID, "--reason", "test restore", "--idempotency-key", "k-res-1"}, wantJSON: true},
		{name: "checkpoint restore trailing direct and json", args: []string{"checkpoint", "restore", targetID, snapID, "--reason", "test restore", "--idempotency-key", "k-res-2", "--direct", "--json"}, wantJSON: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := appInstance.Run(tc.args, &stdout, &stderr)
			if code != cli.ExitSuccess {
				t.Fatalf("expected ExitSuccess (0), got %d. stderr: %s", code, stderr.String())
			}

			if tc.wantJSON {
				var raw json.RawMessage
				if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
					t.Fatalf("expected valid JSON output, got error: %v. stdout: %s", err, stdout.String())
				}
			}
		})
	}
}

func TestCLI_MissingStateDirValue_FailsWithUsage(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"state-dir bare", []string{"--state-dir"}},
		{"state-dir bare with command", []string{"--state-dir", "doctor"}},
		{"state-dir empty equals", []string{"--state-dir="}},
		{"single dash state-dir empty equals", []string{"-state-dir="}},
		{"state-dir empty equals with command", []string{"--state-dir=", "doctor"}},
		{"trailing state-dir after command", []string{"machine", "list", "--state-dir"}},
	}

	backend := &mockBackend{}
	appInstance := setupTestApp(t, backend, nil)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := appInstance.Run(tc.args, &stdout, &stderr)
			if code != cli.ExitUsage {
				t.Fatalf("expected ExitUsage (2) for missing state-dir value, got %d. stderr: %s", code, stderr.String())
			}
		})
	}
}

func TestCLI_UnknownFlags_FailsWithUsage(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	cases := []struct {
		name string
		args []string
	}{
		{"unknown leading flag", []string{"--unknown-flag", "doctor"}},
		{"unknown trailing flag on doctor", []string{"doctor", "--unknown-flag"}},
		{"unknown intermediate flag on machine", []string{"machine", "--unknown-flag", "list"}},
		{"unknown trailing flag on machine list", []string{"machine", "list", "--unknown-flag"}},
		{"unknown trailing flag on machine inspect", []string{"machine", "inspect", targetID, "--unknown-flag"}},
	}

	backend := &mockBackend{}
	appInstance := setupTestApp(t, backend, nil)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := appInstance.Run(tc.args, &stdout, &stderr)
			if code != cli.ExitUsage {
				t.Fatalf("expected ExitUsage (2) for unknown flag %v, got %d. stderr: %s", tc.args, code, stderr.String())
			}
		})
	}
}
