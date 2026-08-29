package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/cli"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestJSONShape_Doctor_CapabilitiesArray(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)

	t.Run("ready with capabilities", func(t *testing.T) {
		mock := &mockObserver{doctorReport: app.NewReadyReport(domain.ReadOnlyMachineCapabilities(), now)}
		cliApp := cli.NewApp(app.NewDiscoveryService(mock))
		var stdout, stderr bytes.Buffer
		code := cliApp.Run([]string{"doctor", "--json"}, &stdout, &stderr)
		if code != cli.ExitSuccess {
			t.Fatalf("expected 0, got %d", code)
		}

		var raw map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
			t.Fatalf("failed to decode JSON: %v", err)
		}
		caps, ok := raw["capabilities"].([]any)
		if !ok || len(caps) != 4 {
			t.Fatalf("expected capabilities array of length 4, got: %v", raw["capabilities"])
		}
	})

	t.Run("unavailable with empty capabilities array", func(t *testing.T) {
		mock := &mockObserver{
			doctorReport: app.NewUnavailableReport(
				app.DoctorReasonHostUnavailable,
				"host unavailable",
				now,
			),
		}
		cliApp := cli.NewApp(app.NewDiscoveryService(mock))
		var stdout, stderr bytes.Buffer
		code := cliApp.Run([]string{"doctor", "--json"}, &stdout, &stderr)
		if code != cli.ExitSuccess {
			t.Fatalf("expected 0, got %d", code)
		}

		var raw map[string]any
		if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
			t.Fatalf("failed to decode JSON: %v", err)
		}
		caps, ok := raw["capabilities"].([]any)
		if !ok || len(caps) != 0 {
			t.Fatalf("expected capabilities empty array [], got: %v", raw["capabilities"])
		}
		// Confirm exact string has `"capabilities": []`
		if !strings.Contains(stdout.String(), `"capabilities": []`) {
			t.Errorf("expected JSON to contain '\"capabilities\": []', got:\n%s", stdout.String())
		}
	})
}

func TestJSONShape_MachineList_EmptyMachinesArray(t *testing.T) {
	mock := &mockObserver{listResult: []domain.MachineObservation{}}
	cliApp := cli.NewApp(app.NewDiscoveryService(mock))
	var stdout, stderr bytes.Buffer
	code := cliApp.Run([]string{"machine", "list", "--json"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("expected 0, got %d", code)
	}

	var raw map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		t.Fatalf("failed to decode JSON: %v", err)
	}
	machines, ok := raw["machines"].([]any)
	if !ok || len(machines) != 0 {
		t.Fatalf("expected machines empty array [], got: %v", raw["machines"])
	}
	if !strings.Contains(stdout.String(), `"machines": []`) {
		t.Errorf("expected JSON to contain '\"machines\": []', got:\n%s", stdout.String())
	}
}

func TestJSONShape_MachineInspect_EmptyArraysPreserved(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	// Machine with no network adapters and empty capabilities
	obs := domain.MachineObservation{
		ID:                  targetID,
		Name:                "vm-bare",
		State:               domain.MachineStateOff,
		RawState:            "Off",
		Generation:          2,
		Version:             "10.0",
		UptimeMs:            0,
		CPUUsagePercent:     0,
		MemoryAssignedBytes: 1048576,
		NetworkAdapters:     []domain.NetworkAdapterSummary{},
		Capabilities:        domain.NewCapabilitySet(),
		ObservedAt:          now,
		ObservationType:     domain.ObservationObserved,
	}

	mock := &mockObserver{
		inspectFn: func(_ context.Context, _ string) (domain.MachineObservation, error) {
			return obs, nil
		},
	}
	cliApp := cli.NewApp(app.NewDiscoveryService(mock))
	var stdout, stderr bytes.Buffer
	code := cliApp.Run([]string{"machine", "inspect", targetID, "--json"}, &stdout, &stderr)
	if code != cli.ExitSuccess {
		t.Fatalf("expected 0, got %d", code)
	}

	outStr := stdout.String()
	if !strings.Contains(outStr, `"network_adapters": []`) {
		t.Errorf("expected '\"network_adapters\": []' in JSON output, got:\n%s", outStr)
	}
	if !strings.Contains(outStr, `"capabilities": []`) {
		t.Errorf("expected '\"capabilities\": []' in JSON output, got:\n%s", outStr)
	}
}

func TestJSONShape_NetworkAdapter_IPAddressesArray(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	// Machine with adapter having empty IP addresses
	obs := domain.MachineObservation{
		ID:                  targetID,
		Name:                "vm-adapter-no-ips",
		State:               domain.MachineStateRunning,
		RawState:            "Running",
		Generation:          2,
		Version:             "10.0",
		UptimeMs:            1000,
		CPUUsagePercent:     1,
		MemoryAssignedBytes: 2097152,
		NetworkAdapters: []domain.NetworkAdapterSummary{
			{
				Name:        "Adapter 1",
				SwitchName:  "Default Switch",
				MACAddress:  "00155D010203",
				IPAddresses: []string{},
				Status:      "OK",
			},
		},
		Capabilities:    domain.ReadOnlyMachineCapabilities(),
		ObservedAt:      now,
		ObservationType: domain.ObservationObserved,
	}

	dto := cli.ConvertToMachineDTO(obs)
	data, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("failed to marshal DTO: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}

	adapters, ok := raw["network_adapters"].([]any)
	if !ok || len(adapters) != 1 {
		t.Fatalf("expected 1 network adapter, got: %v", raw["network_adapters"])
	}

	adapterMap, ok := adapters[0].(map[string]any)
	if !ok {
		t.Fatalf("expected adapter object, got: %v", adapters[0])
	}

	ips, ok := adapterMap["ip_addresses"].([]any)
	if !ok {
		t.Fatalf("expected 'ip_addresses' to be serialized as array, got: %v", adapterMap["ip_addresses"])
	}
	if len(ips) != 0 {
		t.Fatalf("expected empty 'ip_addresses' array, got: %v", ips)
	}

	// Verify exact json string contains "ip_addresses": []
	if !strings.Contains(string(data), `"ip_addresses":[]`) && !strings.Contains(string(data), `"ip_addresses": []`) {
		t.Errorf("expected JSON to contain '\"ip_addresses\":[]', got:\n%s", string(data))
	}
}
