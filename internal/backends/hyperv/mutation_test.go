package hyperv_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/backends/hyperv"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestAdapter_StartMachine_Success(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	jsonPayload := `{
		"schema_version": "1",
		"success": true,
		"machine": {
			"id": "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
			"name": "win11-start-target",
			"state": "Running",
			"status": "Operating normally",
			"generation": 2,
			"version": "10.0",
			"uptime_ms": 1000,
			"cpu_usage": 5,
			"memory_assigned_bytes": 4294967296,
			"network_adapters": []
		}
	}`

	var capturedEnv []string
	mock := &mockExecutor{
		executeFn: func(_ context.Context, _ string, _ []string, env []string) ([]byte, []byte, error) {
			capturedEnv = env
			return []byte(jsonPayload), nil, nil
		},
	}

	adapter := hyperv.New(
		hyperv.WithExecutor(mock),
		hyperv.WithNowFunc(func() time.Time { return now }),
	)

	vm, err := adapter.StartMachine(context.Background(), targetID)
	if err != nil {
		t.Fatalf("unexpected StartMachine error: %v", err)
	}

	if vm.ID != targetID {
		t.Errorf("expected ID %q, got %q", targetID, vm.ID)
	}
	if vm.State != domain.MachineStateRunning {
		t.Errorf("expected State Running, got %q", vm.State)
	}

	foundTargetEnv := false
	for _, env := range capturedEnv {
		if env == "AMC_TARGET_VM_ID="+targetID {
			foundTargetEnv = true
		}
	}
	if !foundTargetEnv {
		t.Errorf("expected AMC_TARGET_VM_ID in env, got %v", capturedEnv)
	}
}

func TestAdapter_StartMachine_Errors(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	cases := []struct {
		name        string
		payload     string
		execErr     error
		expectedErr error
	}{
		{
			name:        "MachineNotFound",
			payload:     `{"schema_version": "1", "error_category": "machine_not_found"}`,
			expectedErr: hyperv.ErrMachineNotFound,
		},
		{
			name:        "AccessDenied",
			payload:     `{"schema_version": "1", "error_category": "access_denied"}`,
			expectedErr: hyperv.ErrAccessDenied,
		},
		{
			name:        "InvalidState",
			payload:     `{"schema_version": "1", "error_category": "invalid_state"}`,
			expectedErr: hyperv.ErrInvalidState,
		},
		{
			name:        "OutputExceededLimit",
			execErr:     hyperv.ErrOutputExceededLimit,
			expectedErr: hyperv.ErrOutputExceededLimit,
		},
		{
			name:        "CommandTimeout",
			execErr:     hyperv.ErrCommandTimeout,
			expectedErr: hyperv.ErrCommandTimeout,
		},
		{
			name:        "MalformedJSON",
			payload:     `not-json`,
			expectedErr: hyperv.ErrMalformedResponse,
		},
		{
			name:        "TrailingData",
			payload:     `{"schema_version": "1", "success": true, "machine": {"id": "c4a523d4-6b99-4d62-a5e2-4752c0f20001", "name": "w", "state": "Running", "generation": 2, "version": "1", "uptime_ms": 0, "cpu_usage": 0, "memory_assigned_bytes": 0}} extra`,
			expectedErr: hyperv.ErrTrailingData,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockExecutor{
				executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
					return []byte(tc.payload), nil, tc.execErr
				},
			}
			adapter := hyperv.New(hyperv.WithExecutor(mock))
			_, err := adapter.StartMachine(context.Background(), targetID)
			if err == nil || !errors.Is(err, tc.expectedErr) {
				t.Fatalf("expected error %v, got %v", tc.expectedErr, err)
			}
		})
	}
}

func TestAdapter_StopMachine_Modes(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	jsonPayload := `{
		"schema_version": "1",
		"success": true,
		"machine": {
			"id": "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
			"name": "win11-stop-target",
			"state": "Off",
			"generation": 2,
			"version": "10.0",
			"uptime_ms": 0,
			"cpu_usage": 0,
			"memory_assigned_bytes": 0,
			"network_adapters": []
		}
	}`

	modes := []string{"shutdown", "save", "turn-off"}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			var capturedEnv []string
			mock := &mockExecutor{
				executeFn: func(_ context.Context, _ string, _ []string, env []string) ([]byte, []byte, error) {
					capturedEnv = env
					return []byte(jsonPayload), nil, nil
				},
			}
			adapter := hyperv.New(
				hyperv.WithExecutor(mock),
				hyperv.WithNowFunc(func() time.Time { return now }),
			)

			vm, err := adapter.StopMachine(context.Background(), targetID, mode)
			if err != nil {
				t.Fatalf("unexpected StopMachine error: %v", err)
			}
			if vm.State != domain.MachineStateOff {
				t.Errorf("expected State Off, got %q", vm.State)
			}

			foundMode := false
			for _, env := range capturedEnv {
				if strings.Contains(env, "AMC_STOP_MODE="+mode) {
					foundMode = true
				}
			}
			if !foundMode {
				t.Errorf("expected AMC_STOP_MODE=%s in env, got %v", mode, capturedEnv)
			}
		})
	}
}

func TestAdapter_RestoreCheckpoint_SuccessAndErrors(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	snapID := "d4a523d4-6b99-4d62-a5e2-4752c0f20002"

	jsonSuccess := `{
		"schema_version": "1",
		"success": true,
		"machine": {
			"id": "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
			"name": "win11-restore-target",
			"state": "Running",
			"generation": 2,
			"version": "10.0",
			"uptime_ms": 100,
			"cpu_usage": 0,
			"memory_assigned_bytes": 2147483648,
			"network_adapters": []
		}
	}`

	mock := &mockExecutor{
		executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
			return []byte(jsonSuccess), nil, nil
		},
	}
	adapter := hyperv.New(
		hyperv.WithExecutor(mock),
		hyperv.WithNowFunc(func() time.Time { return now }),
	)

	vm, err := adapter.RestoreCheckpoint(context.Background(), targetID, snapID)
	if err != nil {
		t.Fatalf("unexpected RestoreCheckpoint error: %v", err)
	}
	if vm.ID != targetID {
		t.Errorf("expected target ID %s, got %s", targetID, vm.ID)
	}

	// Checkpoint not found error
	mockNotFound := &mockExecutor{
		executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
			return []byte(`{"schema_version": "1", "error_category": "checkpoint_not_found"}`), nil, nil
		},
	}
	adapterNotFound := hyperv.New(hyperv.WithExecutor(mockNotFound))
	_, err = adapterNotFound.RestoreCheckpoint(context.Background(), targetID, snapID)
	if err == nil || !errors.Is(err, hyperv.ErrCheckpointNotFound) {
		t.Fatalf("expected ErrCheckpointNotFound, got %v", err)
	}
}

func TestAdapter_MutationValidationAndCategories(t *testing.T) {
	adapter := hyperv.New(hyperv.WithExecutor(&mockExecutor{}))
	ctx := context.Background()

	// Invalid GUIDs
	if _, err := adapter.StartMachine(ctx, "invalid-guid"); err == nil {
		t.Errorf("expected error for invalid GUID in StartMachine")
	}
	if _, err := adapter.StopMachine(ctx, "invalid-guid", "shutdown"); err == nil {
		t.Errorf("expected error for invalid GUID in StopMachine")
	}
	if _, err := adapter.ListCheckpoints(ctx, "invalid-guid"); err == nil {
		t.Errorf("expected error for invalid GUID in ListCheckpoints")
	}
	if _, err := adapter.CreateCheckpoint(ctx, "invalid-guid", "snap"); err == nil {
		t.Errorf("expected error for invalid GUID in CreateCheckpoint")
	}
	if _, err := adapter.RestoreCheckpoint(ctx, "invalid-guid", "e4a523d4-6b99-4d62-a5e2-4752c0f20001"); err == nil {
		t.Errorf("expected error for invalid VM GUID in RestoreCheckpoint")
	}
	if _, err := adapter.RestoreCheckpoint(ctx, "c4a523d4-6b99-4d62-a5e2-4752c0f20001", "invalid-chk-guid"); err == nil {
		t.Errorf("expected error for invalid chk GUID in RestoreCheckpoint")
	}

	// Unrecognized mode in StopMachine
	if _, err := adapter.StopMachine(ctx, "c4a523d4-6b99-4d62-a5e2-4752c0f20001", "invalid-mode"); err == nil {
		t.Errorf("expected error for invalid mode in StopMachine")
	}
}
