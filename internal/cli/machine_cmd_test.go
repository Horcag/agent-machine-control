package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/backends/hyperv"
	"github.com/Horcag/agent-machine-control/internal/cli"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestApp_Machine_SubcommandRouting(t *testing.T) {
	mock := &mockObserver{}
	service := app.NewDiscoveryService(mock)
	cliApp := cli.NewApp(service)

	t.Run("missing subcommand", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := cliApp.Run([]string{"machine"}, &stdout, &stderr)
		if code != cli.ExitUsage {
			t.Fatalf("expected ExitUsage (2), got %d", code)
		}
	})

	t.Run("unknown subcommand", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := cliApp.Run([]string{"machine", "invalid"}, &stdout, &stderr)
		if code != cli.ExitUsage {
			t.Fatalf("expected ExitUsage (2), got %d", code)
		}
	})

	t.Run("help subcommand", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := cliApp.Run([]string{"machine", "help"}, &stdout, &stderr)
		if code != cli.ExitSuccess {
			t.Fatalf("expected ExitSuccess (0), got %d", code)
		}
		if !strings.Contains(stdout.String(), "Usage: amc machine") {
			t.Errorf("expected machine usage, got %q", stdout.String())
		}
	})
}

func sampleVMs(now time.Time) []domain.MachineObservation {
	return []domain.MachineObservation{
		{
			ID:                  "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
			Name:                "zebra-vm",
			State:               domain.MachineStateRunning,
			RawState:            "Running",
			Generation:          2,
			Version:             "10.0",
			UptimeMs:            7200000,
			CPUUsagePercent:     3,
			MemoryAssignedBytes: 4294967296,
			Capabilities:        domain.ReadOnlyMachineCapabilities(),
			ObservedAt:          now,
			ObservationType:     domain.ObservationObserved,
		},
		{
			ID:                  "e7b123d4-6b99-4d62-a5e2-4752c0f20002",
			Name:                "alpha-vm",
			State:               domain.MachineStateOff,
			RawState:            "Off",
			Generation:          1,
			Version:             "9.0",
			UptimeMs:            0,
			CPUUsagePercent:     0,
			MemoryAssignedBytes: 2147483648,
			Capabilities:        domain.ReadOnlyMachineCapabilities(),
			ObservedAt:          now,
			ObservationType:     domain.ObservationObserved,
		},
	}
}

func TestApp_MachineList_Success_Human(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	mock := &mockObserver{listResult: sampleVMs(now)}
	service := app.NewDiscoveryService(mock)
	cliApp := cli.NewApp(service)

	var stdout, stderr bytes.Buffer
	code := cliApp.Run([]string{"machine", "list"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("expected ExitSuccess, got %d", code)
	}
	out := stdout.String()
	if !strings.Contains(out, "ID") || !strings.Contains(out, "NAME") || !strings.Contains(out, "STATE") {
		t.Errorf("expected column headers, got %q", out)
	}
	alphaIdx := strings.Index(out, "alpha-vm")
	zebraIdx := strings.Index(out, "zebra-vm")
	if alphaIdx == -1 || zebraIdx == -1 || alphaIdx > zebraIdx {
		t.Errorf("expected alpha-vm before zebra-vm in sorted output, got %q", out)
	}
}

func TestApp_MachineList_Success_JSON(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	mock := &mockObserver{listResult: sampleVMs(now)}
	service := app.NewDiscoveryService(mock)
	cliApp := cli.NewApp(service)

	var stdout, stderr bytes.Buffer
	code := cliApp.Run([]string{"machine", "list", "--json"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("expected ExitSuccess, got %d", code)
	}
	var env cli.MachineListOutputEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("failed to decode list JSON: %v", err)
	}
	if env.SchemaVersion != "1" || env.ObservationType != domain.ObservationObserved {
		t.Errorf("unexpected list envelope headers: %+v", env)
	}
	if len(env.Machines) != 2 {
		t.Fatalf("expected 2 machines, got %d", len(env.Machines))
	}
	if env.Machines[0].Name != "alpha-vm" || env.Machines[1].Name != "zebra-vm" {
		t.Errorf("expected sorted machines in JSON: %+v", env.Machines)
	}
	if len(env.Machines[0].Capabilities) != 4 {
		t.Errorf("expected 4 capabilities in machine DTO, got %d", len(env.Machines[0].Capabilities))
	}
}

func TestApp_MachineList_Empty(t *testing.T) {
	mock := &mockObserver{
		listResult: []domain.MachineObservation{},
	}
	service := app.NewDiscoveryService(mock)
	cliApp := cli.NewApp(service)

	t.Run("human empty", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := cliApp.Run([]string{"machine", "list"}, &stdout, &stderr)
		if code != cli.ExitSuccess {
			t.Fatalf("expected ExitSuccess, got %d", code)
		}
		if !strings.Contains(stdout.String(), "No virtual machines found.") {
			t.Errorf("expected 'No virtual machines found.', got %q", stdout.String())
		}
	})

	t.Run("JSON empty", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := cliApp.Run([]string{"machine", "list", "--json"}, &stdout, &stderr)
		if code != cli.ExitSuccess {
			t.Fatalf("expected ExitSuccess, got %d", code)
		}
		var env cli.MachineListOutputEnvelope
		if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatalf("failed to decode list JSON: %v", err)
		}
		if len(env.Machines) != 0 {
			t.Errorf("expected 0 machines in JSON, got %d", len(env.Machines))
		}
	})
}

func TestApp_MachineList_Errors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{"backend unavailable", hyperv.ErrBackendUnavailable, cli.ExitBackendUnavailable},
		{"access denied", hyperv.ErrAccessDenied, cli.ExitBackendUnavailable},
		{"module missing", hyperv.ErrModuleMissing, cli.ExitBackendUnavailable},
		{"malformed provider", hyperv.ErrMalformedResponse, cli.ExitMalformedProvider},
		{"timeout", hyperv.ErrCommandTimeout, cli.ExitTimeout},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockObserver{listErr: tt.err}
			service := app.NewDiscoveryService(mock)
			cliApp := cli.NewApp(service)

			var stdout, stderr bytes.Buffer
			code := cliApp.Run([]string{"machine", "list"}, &stdout, &stderr)
			if code != tt.wantCode {
				t.Errorf("expected exit code %d, got %d", tt.wantCode, code)
			}
			if stderr.Len() == 0 {
				t.Errorf("expected error message on stderr")
			}
		})
	}
}

func sampleInspectVM(targetID string, now time.Time) domain.MachineObservation {
	return domain.MachineObservation{
		ID:                  targetID,
		Name:                "ubuntu-test",
		State:               domain.MachineStateRunning,
		RawState:            "Running",
		RawStatus:           "Operating normally",
		Generation:          2,
		Version:             "10.0",
		UptimeMs:            7200000,
		CPUUsagePercent:     4,
		MemoryAssignedBytes: 4294967296,
		NetworkAdapters: []domain.NetworkAdapterSummary{
			{
				Name:        "eth0",
				SwitchName:  "Default Switch",
				MACAddress:  "00155D010203",
				IPAddresses: []string{"172.20.10.5"},
				Status:      "OK",
			},
		},
		Capabilities:    domain.ReadOnlyMachineCapabilities(),
		ObservedAt:      now,
		ObservationType: domain.ObservationObserved,
	}
}

func TestApp_MachineInspect_Success_Human(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	vm := sampleInspectVM(targetID, now)

	mock := &mockObserver{
		inspectFn: func(_ context.Context, id string) (domain.MachineObservation, error) {
			if id == targetID {
				return vm, nil
			}
			return domain.MachineObservation{}, hyperv.ErrMachineNotFound
		},
	}
	service := app.NewDiscoveryService(mock)
	cliApp := cli.NewApp(service)

	var stdout, stderr bytes.Buffer
	code := cliApp.Run([]string{"machine", "inspect", targetID}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("expected ExitSuccess, got %d", code)
	}
	out := stdout.String()
	if !strings.Contains(out, targetID) || !strings.Contains(out, "ubuntu-test") {
		t.Errorf("expected ID and Name in inspect output, got %q", out)
	}
	if !strings.Contains(out, "eth0") || !strings.Contains(out, "172.20.10.5") {
		t.Errorf("expected network adapter info in inspect output, got %q", out)
	}
}

func TestApp_MachineInspect_Success_JSON(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	vm := sampleInspectVM(targetID, now)

	mock := &mockObserver{
		inspectFn: func(_ context.Context, id string) (domain.MachineObservation, error) {
			if id == targetID {
				return vm, nil
			}
			return domain.MachineObservation{}, hyperv.ErrMachineNotFound
		},
	}
	service := app.NewDiscoveryService(mock)
	cliApp := cli.NewApp(service)

	var stdout, stderr bytes.Buffer
	code := cliApp.Run([]string{"machine", "inspect", targetID, "--json"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("expected ExitSuccess, got %d", code)
	}
	var env cli.MachineInspectOutputEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("failed to decode inspect JSON: %v", err)
	}
	if env.SchemaVersion != "1" || env.Machine.ID != targetID || env.Machine.Name != "ubuntu-test" {
		t.Errorf("unexpected inspect envelope: %+v", env)
	}
	if len(env.Machine.Capabilities) != 4 {
		t.Errorf("expected 4 capabilities in inspect machine DTO, got %d", len(env.Machine.Capabilities))
	}
}

func TestApp_MachineInspect_ValidationAndErrors(t *testing.T) {
	mock := &mockObserver{
		inspectFn: func(_ context.Context, _ string) (domain.MachineObservation, error) {
			return domain.MachineObservation{}, hyperv.ErrMachineNotFound
		},
	}
	service := app.NewDiscoveryService(mock)
	cliApp := cli.NewApp(service)

	t.Run("missing GUID", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := cliApp.Run([]string{"machine", "inspect"}, &stdout, &stderr)
		if code != cli.ExitUsage {
			t.Errorf("expected ExitUsage (2), got %d", code)
		}
	})

	t.Run("invalid GUID format", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := cliApp.Run([]string{"machine", "inspect", "not-a-guid"}, &stdout, &stderr)
		if code != cli.ExitUsage {
			t.Errorf("expected ExitUsage (2), got %d", code)
		}
		if !strings.Contains(stderr.String(), "invalid machine GUID") {
			t.Errorf("expected invalid GUID message on stderr, got %q", stderr.String())
		}
	})

	t.Run("machine not found", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := cliApp.Run([]string{"machine", "inspect", "c4a523d4-6b99-4d62-a5e2-4752c0f20001"}, &stdout, &stderr)
		if code != cli.ExitNotFound {
			t.Errorf("expected ExitNotFound (3), got %d", code)
		}
		if !strings.Contains(stderr.String(), "machine not found") {
			t.Errorf("expected not found message on stderr, got %q", stderr.String())
		}
	})
}

func TestApp_MachineDTO_DeterministicSorting(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	vm := domain.MachineObservation{
		ID:                  "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Name:                "vm-sort-test",
		State:               domain.MachineStateRunning,
		RawState:            "Running",
		Generation:          2,
		Version:             "10.0",
		UptimeMs:            1000,
		CPUUsagePercent:     2,
		MemoryAssignedBytes: 1024,
		NetworkAdapters: []domain.NetworkAdapterSummary{
			{
				Name:        "eth1",
				MACAddress:  "00155D010202",
				IPAddresses: []string{"192.168.1.50", "10.0.0.1"},
			},
			{
				Name:        "eth0",
				MACAddress:  "00155D010201",
				IPAddresses: []string{"172.16.0.2", "10.0.0.2"},
			},
		},
		Capabilities:    domain.ReadOnlyMachineCapabilities(),
		ObservedAt:      now,
		ObservationType: domain.ObservationObserved,
	}

	dto := cli.ConvertToMachineDTO(vm)

	// Adapters sorted: eth0 before eth1
	if dto.NetworkAdapters[0].Name != "eth0" || dto.NetworkAdapters[1].Name != "eth1" {
		t.Errorf("expected eth0 before eth1, got %+v", dto.NetworkAdapters)
	}

	// IP addresses sorted: 10.0.0.2 before 172.16.0.2
	if dto.NetworkAdapters[0].IPAddresses[0] != "10.0.0.2" || dto.NetworkAdapters[0].IPAddresses[1] != "172.16.0.2" {
		t.Errorf("expected sorted IPs on eth0, got %+v", dto.NetworkAdapters[0].IPAddresses)
	}
}

func TestApp_MachineInspect_SanitizedErrorOutput(t *testing.T) {
	mock := &mockObserver{
		inspectFn: func(_ context.Context, _ string) (domain.MachineObservation, error) {
			return domain.MachineObservation{}, errors.New("secret-host-path-C:\\internal\\token\\leak")
		},
	}
	service := app.NewDiscoveryService(mock)
	cliApp := cli.NewApp(service)

	var stdout, stderr bytes.Buffer
	code := cliApp.Run([]string{"machine", "inspect", "c4a523d4-6b99-4d62-a5e2-4752c0f20001"}, &stdout, &stderr)
	if code != cli.ExitBackendUnavailable {
		t.Fatalf("expected ExitBackendUnavailable (4), got %d", code)
	}
	if strings.Contains(stderr.String(), "secret-host-path") || strings.Contains(stderr.String(), "internal") {
		t.Errorf("stderr leaked raw error string: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Hyper-V host management is unavailable") {
		t.Errorf("expected static unavailable message, got %q", stderr.String())
	}
}
