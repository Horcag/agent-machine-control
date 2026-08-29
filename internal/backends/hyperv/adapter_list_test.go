package hyperv_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/backends/hyperv"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestAdapter_ListMachines_Success(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	jsonPayload := `{
		"schema_version": "1",
		"machines": [
			{
				"id": "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
				"name": "ubuntu-2204",
				"state": "Running",
				"status": "Operating normally",
				"generation": 2,
				"version": "10.0",
				"uptime_ms": 7200000,
				"cpu_usage": 3,
				"memory_assigned_bytes": 4294967296,
				"network_adapters": [
					{
						"name": "Network Adapter",
						"switch_name": "Default Switch",
						"mac_address": "00155D010203",
						"ip_addresses": ["172.20.10.5", "fe80::1"],
						"status": "OK"
					}
				]
			}
		]
	}`

	mock := &mockExecutor{
		executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
			return []byte(jsonPayload), nil, nil
		},
	}

	adapter := hyperv.New(
		hyperv.WithExecutor(mock),
		hyperv.WithNowFunc(func() time.Time { return now }),
	)

	vms, err := adapter.ListMachines(context.Background())
	if err != nil {
		t.Fatalf("unexpected ListMachines error: %v", err)
	}

	if len(vms) != 1 {
		t.Fatalf("expected 1 machine, got %d", len(vms))
	}

	vm := vms[0]
	if vm.ID != "c4a523d4-6b99-4d62-a5e2-4752c0f20001" {
		t.Errorf("expected ID %q, got %q", "c4a523d4-6b99-4d62-a5e2-4752c0f20001", vm.ID)
	}
	if vm.Name != "ubuntu-2204" {
		t.Errorf("expected Name %q, got %q", "ubuntu-2204", vm.Name)
	}
	if vm.State != domain.MachineStateRunning {
		t.Errorf("expected State %q, got %q", domain.MachineStateRunning, vm.State)
	}
	if vm.RawState != "Running" {
		t.Errorf("expected RawState %q, got %q", "Running", vm.RawState)
	}
	if vm.MemoryAssignedBytes != 4294967296 {
		t.Errorf("expected Memory %d, got %d", 4294967296, vm.MemoryAssignedBytes)
	}
	if len(vm.NetworkAdapters) != 1 {
		t.Fatalf("expected 1 network adapter, got %d", len(vm.NetworkAdapters))
	}
	if vm.NetworkAdapters[0].MACAddress != "00155D010203" {
		t.Errorf("expected MAC '00155D010203', got %q", vm.NetworkAdapters[0].MACAddress)
	}
}

func TestAdapter_ListMachines_EmptyInventory(t *testing.T) {
	mock := &mockExecutor{
		executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
			return []byte(`{"schema_version":"1","machines":[]}`), nil, nil
		},
	}
	adapter := hyperv.New(hyperv.WithExecutor(mock))
	vms, err := adapter.ListMachines(context.Background())
	if err != nil {
		t.Fatalf("expected nil error on empty machines array, got %v", err)
	}
	if len(vms) != 0 {
		t.Fatalf("expected 0 machines, got %d", len(vms))
	}
}

func TestAdapter_ListMachines_NullMachinesRejected(t *testing.T) {
	mock := &mockExecutor{
		executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
			return []byte(`{"schema_version":"1","machines":null}`), nil, nil
		},
	}
	adapter := hyperv.New(hyperv.WithExecutor(mock))
	_, err := adapter.ListMachines(context.Background())
	if !errors.Is(err, hyperv.ErrMalformedResponse) {
		t.Fatalf("expected ErrMalformedResponse for null machines, got %v", err)
	}
}

func TestAdapter_ListMachines_SingletonNormalization(t *testing.T) {
	singleJSON := `{
		"schema_version": "1",
		"machines": {
			"id": "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
			"name": "win11-singleton",
			"state": "Off",
			"generation": 2,
			"version": "10.0",
			"uptime_ms": 0,
			"cpu_usage": 0,
			"memory_assigned_bytes": 2147483648
		}
	}`
	mock := &mockExecutor{
		executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
			return []byte(singleJSON), nil, nil
		},
	}
	adapter := hyperv.New(hyperv.WithExecutor(mock))
	vms, err := adapter.ListMachines(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vms) != 1 || vms[0].State != domain.MachineStateOff {
		t.Fatalf("expected 1 machine with state off, got %+v", vms)
	}
}

func TestAdapter_ListMachines_MalformedJSON(t *testing.T) {
	tests := []struct {
		name      string
		payload   string
		wantErrIs error
	}{
		{
			name:      "duplicate ID",
			payload:   `{"schema_version":"1","machines":[{"id":"c4a523d4-6b99-4d62-a5e2-4752c0f20001","name":"v1","state":"Off","generation":2,"version":"10.0"},{"id":"c4a523d4-6b99-4d62-a5e2-4752c0f20001","name":"v2","state":"Off","generation":2,"version":"10.0"}]}`,
			wantErrIs: hyperv.ErrDuplicateMachineID,
		},
		{
			name:      "trailing data",
			payload:   `{"schema_version":"1","machines":[]} extra`,
			wantErrIs: hyperv.ErrTrailingData,
		},
		{
			name:      "unknown field",
			payload:   `{"schema_version":"1","machines":[],"unexpected_field":123}`,
			wantErrIs: hyperv.ErrMalformedResponse,
		},
		{
			name:      "unknown category",
			payload:   `{"schema_version":"1","error_category":"unknown_code"}`,
			wantErrIs: hyperv.ErrMalformedResponse,
		},
		{
			name:      "machine_not_found rejected in list",
			payload:   `{"schema_version":"1","error_category":"machine_not_found"}`,
			wantErrIs: hyperv.ErrMalformedResponse,
		},
		{
			name:      "both machines and error_category",
			payload:   `{"schema_version":"1","machines":[],"error_category":"access_denied"}`,
			wantErrIs: hyperv.ErrMalformedResponse,
		},
		{
			name:      "neither machines nor error_category",
			payload:   `{"schema_version":"1"}`,
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
			_, err := adapter.ListMachines(context.Background())
			if !errors.Is(err, tt.wantErrIs) {
				t.Fatalf("expected %v, got %v", tt.wantErrIs, err)
			}
		})
	}
}

func TestAdapter_ListMachines_StructuredErrors(t *testing.T) {
	tests := []struct {
		name      string
		payload   string
		wantErrIs error
	}{
		{"access denied", `{"schema_version":"1","error_category":"access_denied"}`, hyperv.ErrAccessDenied},
		{"module missing", `{"schema_version":"1","error_category":"module_missing"}`, hyperv.ErrModuleMissing},
		{"host service unavailable", `{"schema_version":"1","error_category":"host_unavailable"}`, hyperv.ErrHostUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockExecutor{
				executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
					return []byte(tt.payload), []byte("Localized error text on stderr"), nil
				},
			}
			adapter := hyperv.New(hyperv.WithExecutor(mock))
			_, err := adapter.ListMachines(context.Background())
			if !errors.Is(err, tt.wantErrIs) {
				t.Errorf("expected %v, got %v", tt.wantErrIs, err)
			}
		})
	}
}

func TestAdapter_ListMachines_OutputLimitExceeded(t *testing.T) {
	mock := &mockExecutor{
		executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
			return nil, nil, hyperv.ErrOutputExceededLimit
		},
	}
	adapter := hyperv.New(hyperv.WithExecutor(mock))
	_, err := adapter.ListMachines(context.Background())
	if !errors.Is(err, hyperv.ErrOutputExceededLimit) {
		t.Fatalf("expected ErrOutputExceededLimit, got %v", err)
	}
}

func TestAdapter_ListMachines_NonZeroExitWithValidJSON(t *testing.T) {
	validJSON := `{"schema_version":"1","machines":[{"id":"c4a523d4-6b99-4d62-a5e2-4752c0f20001","name":"v1","state":"Running","generation":2,"version":"10.0"}]}`
	mock := &mockExecutor{
		executeFn: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
			return []byte(validJSON), []byte("error output"), errors.New("exit status 1")
		},
	}
	adapter := hyperv.New(hyperv.WithExecutor(mock))
	_, err := adapter.ListMachines(context.Background())
	if !errors.Is(err, hyperv.ErrHostUnavailable) {
		t.Fatalf("expected ErrHostUnavailable on non-zero exit with valid JSON, got %v", err)
	}
}
