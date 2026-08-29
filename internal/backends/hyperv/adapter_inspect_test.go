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

func TestAdapter_InspectMachine_Success(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	jsonPayload := `{
		"schema_version": "1",
		"machine": {
			"id": "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
			"name": "win11-inspect-target",
			"state": "Running",
			"status": "Operating normally",
			"generation": 2,
			"version": "10.0",
			"uptime_ms": 3600000,
			"cpu_usage": 1,
			"memory_assigned_bytes": 8589934592,
			"network_adapters": {
				"name": "Network Adapter",
				"switch_name": "Default Switch",
				"mac_address": "00155D010203",
				"ip_addresses": "172.20.10.5",
				"status": "OK"
			}
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

	vm, err := adapter.InspectMachine(context.Background(), targetID)
	if err != nil {
		t.Fatalf("unexpected InspectMachine error: %v", err)
	}

	if vm.ID != targetID {
		t.Errorf("expected ID %q, got %q", targetID, vm.ID)
	}
	if vm.Name != "win11-inspect-target" {
		t.Errorf("expected Name %q, got %q", "win11-inspect-target", vm.Name)
	}
	if vm.State != domain.MachineStateRunning {
		t.Errorf("expected State %q, got %q", domain.MachineStateRunning, vm.State)
	}
	if vm.MemoryAssignedBytes != 8589934592 {
		t.Errorf("expected Memory %d, got %d", 8589934592, vm.MemoryAssignedBytes)
	}
	if len(vm.NetworkAdapters) != 1 {
		t.Fatalf("expected 1 network adapter, got %d", len(vm.NetworkAdapters))
	}
	if len(vm.NetworkAdapters[0].IPAddresses) != 1 || vm.NetworkAdapters[0].IPAddresses[0] != "172.20.10.5" {
		t.Errorf("expected IP '172.20.10.5', got %+v", vm.NetworkAdapters[0].IPAddresses)
	}

	foundTargetEnv := false
	for _, e := range capturedEnv {
		if strings.HasPrefix(e, "AMC_TARGET_VM_ID=") {
			foundTargetEnv = true
			val := strings.TrimPrefix(e, "AMC_TARGET_VM_ID=")
			if val != targetID {
				t.Errorf("expected AMC_TARGET_VM_ID to be %q, got %q", targetID, val)
			}
		}
	}
	if !foundTargetEnv {
		t.Errorf("expected AMC_TARGET_VM_ID in executor env")
	}
}

func TestAdapter_InspectMachine_InvalidGUID(t *testing.T) {
	mock := &mockExecutor{
		executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
			t.Fatal("executor should not be called on invalid GUID")
			return nil, nil, nil
		},
	}

	adapter := hyperv.New(hyperv.WithExecutor(mock))
	_, err := adapter.InspectMachine(context.Background(), "invalid-guid")
	if err == nil || !errors.Is(err, domain.ErrInvalidMachineID) {
		t.Fatalf("expected ErrInvalidMachineID, got %v", err)
	}
}

func TestAdapter_InspectMachine_NotFound(t *testing.T) {
	mock := &mockExecutor{
		executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
			return []byte(`{"schema_version":"1","error_category":"machine_not_found"}`), nil, nil
		},
	}

	adapter := hyperv.New(hyperv.WithExecutor(mock))
	_, err := adapter.InspectMachine(context.Background(), "c4a523d4-6b99-4d62-a5e2-4752c0f20001")
	if err == nil || !errors.Is(err, hyperv.ErrMachineNotFound) {
		t.Fatalf("expected ErrMachineNotFound, got %v", err)
	}
}

func TestAdapter_InspectMachine_AccessDenied(t *testing.T) {
	mock := &mockExecutor{
		executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
			return []byte(`{"schema_version":"1","error_category":"access_denied"}`), nil, nil
		},
	}

	adapter := hyperv.New(hyperv.WithExecutor(mock))
	_, err := adapter.InspectMachine(context.Background(), "c4a523d4-6b99-4d62-a5e2-4752c0f20001")
	if err == nil || !errors.Is(err, hyperv.ErrAccessDenied) {
		t.Fatalf("expected ErrAccessDenied, got %v", err)
	}
}

func TestAdapter_InspectMachine_OutputLimitExceeded(t *testing.T) {
	mock := &mockExecutor{
		executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
			return nil, nil, hyperv.ErrOutputExceededLimit
		},
	}

	adapter := hyperv.New(hyperv.WithExecutor(mock))
	_, err := adapter.InspectMachine(context.Background(), "c4a523d4-6b99-4d62-a5e2-4752c0f20001")
	if err == nil || !errors.Is(err, hyperv.ErrOutputExceededLimit) {
		t.Fatalf("expected ErrOutputExceededLimit, got %v", err)
	}
}

func TestAdapter_InspectMachine_SchemaVersionMismatch(t *testing.T) {
	payload := `{"schema_version":"2","machine":{"id":"c4a523d4-6b99-4d62-a5e2-4752c0f20001","name":"vm-1","state":"Running","generation":2,"version":"10.0"}}`
	mock := &mockExecutor{
		executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
			return []byte(payload), nil, nil
		},
	}

	adapter := hyperv.New(hyperv.WithExecutor(mock))
	_, err := adapter.InspectMachine(context.Background(), "c4a523d4-6b99-4d62-a5e2-4752c0f20001")
	if err == nil || !errors.Is(err, hyperv.ErrUnexpectedSchemaVersion) {
		t.Fatalf("expected ErrUnexpectedSchemaVersion, got %v", err)
	}
}

func TestAdapter_InspectMachine_EnvelopeExclusivity(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	tests := []struct {
		name      string
		payload   string
		wantErrIs error
	}{
		{
			name:      "both machine and error_category",
			payload:   `{"schema_version":"1","machine":{"id":"c4a523d4-6b99-4d62-a5e2-4752c0f20001","name":"v1","state":"Running","generation":2,"version":"10.0"},"error_category":"access_denied"}`,
			wantErrIs: hyperv.ErrMalformedResponse,
		},
		{
			name:      "neither machine nor error_category",
			payload:   `{"schema_version":"1"}`,
			wantErrIs: hyperv.ErrMalformedResponse,
		},
		{
			name:      "unknown error_category",
			payload:   `{"schema_version":"1","error_category":"unknown_code"}`,
			wantErrIs: hyperv.ErrMalformedResponse,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockExecutor{
				executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
					return []byte(tt.payload), nil, nil
				},
			}
			adapter := hyperv.New(hyperv.WithExecutor(mock))
			_, err := adapter.InspectMachine(context.Background(), targetID)
			if !errors.Is(err, tt.wantErrIs) {
				t.Fatalf("expected %v, got %v", tt.wantErrIs, err)
			}
		})
	}
}

func TestAdapter_InspectMachine_NonZeroExitWithValidJSON(t *testing.T) {
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	validJSON := `{"schema_version":"1","machine":{"id":"c4a523d4-6b99-4d62-a5e2-4752c0f20001","name":"v1","state":"Running","generation":2,"version":"10.0"}}`
	mock := &mockExecutor{
		executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
			return []byte(validJSON), []byte("process failed"), errors.New("exit status 1")
		},
	}
	adapter := hyperv.New(hyperv.WithExecutor(mock))
	_, err := adapter.InspectMachine(context.Background(), targetID)
	if !errors.Is(err, hyperv.ErrHostUnavailable) {
		t.Fatalf("expected ErrHostUnavailable on non-zero exit with valid JSON, got %v", err)
	}
}
