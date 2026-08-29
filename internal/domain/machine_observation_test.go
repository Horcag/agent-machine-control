package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestValidateMachineGUID(t *testing.T) {
	validGUIDs := []string{
		"c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		"00000000-0000-0000-0000-000000000000",
		"FFFFFFFF-FFFF-FFFF-FFFF-FFFFFFFFFFFF",
		"12345678-ABCD-EF01-2345-6789ABCDEF01",
	}

	for _, g := range validGUIDs {
		if err := domain.ValidateMachineGUID(g); err != nil {
			t.Errorf("expected GUID %q to be valid, got %v", g, err)
		}
	}

	invalidGUIDs := []struct {
		id   string
		desc string
	}{
		{"", "empty"},
		{"c4a523d4-6b99-4d62-a5e2-4752c0f2000", "too short (35 chars)"},
		{"c4a523d4-6b99-4d62-a5e2-4752c0f200011", "too long (37 chars)"},
		{"c4a523d4_6b99_4d62_a5e2_4752c0f20001", "underscores instead of hyphens"},
		{"c4a523d4-6b99-4d62-a5e2-4752c0f2000g", "invalid hex char 'g'"},
		{"c4a523d4-6b99-4d62-a5e2-4752c0f2000!", "invalid char '!'"},
	}

	for _, tc := range invalidGUIDs {
		err := domain.ValidateMachineGUID(tc.id)
		if err == nil || !errors.Is(err, domain.ErrInvalidMachineID) {
			t.Errorf("expected ErrInvalidMachineID for %s (%q), got %v", tc.desc, tc.id, err)
		}
	}
}

func TestNormalizeLifecycleState(t *testing.T) {
	tests := []struct {
		raw  string
		want domain.MachineLifecycleState
	}{
		{"Running", domain.MachineStateRunning},
		{"running", domain.MachineStateRunning},
		{"RUNNING", domain.MachineStateRunning},
		{"Off", domain.MachineStateOff},
		{"off", domain.MachineStateOff},
		{"poweroff", domain.MachineStateOff},
		{"poweredoff", domain.MachineStateOff},
		{"Paused", domain.MachineStatePaused},
		{"Saved", domain.MachineStateSaved},
		{"Starting", domain.MachineStateStarting},
		{"Stopping", domain.MachineStateStopping},
		{"Saving", domain.MachineStateSaving},
		{"Pausing", domain.MachineStatePausing},
		{"Resuming", domain.MachineStateResuming},
		{"Unknown", domain.MachineStateUnknown},
		{"Suspended", domain.MachineStateUnknown},
		{"CustomVendorState", domain.MachineStateUnknown},
		{"", domain.MachineStateUnknown},
	}

	for _, tt := range tests {
		got := domain.NormalizeLifecycleState(tt.raw)
		if got != tt.want {
			t.Errorf("NormalizeLifecycleState(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func makeValidObservation() domain.MachineObservation {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	return domain.MachineObservation{
		ID:                  "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Name:                "win11-sandbox",
		State:               domain.MachineStateRunning,
		RawState:            "Running",
		RawStatus:           "Operating normally",
		Generation:          2,
		Version:             "10.0",
		UptimeMs:            120000,
		CPUUsagePercent:     5,
		MemoryAssignedBytes: 4294967296,
		NetworkAdapters: []domain.NetworkAdapterSummary{
			{
				Name:        "Network Adapter",
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

func TestMachineObservation_Validate(t *testing.T) {
	base := makeValidObservation()
	if err := base.Validate(); err != nil {
		t.Fatalf("expected base observation to be valid, got: %v", err)
	}

	tests := []struct {
		name      string
		mutate    func(m *domain.MachineObservation)
		wantErrIs error
	}{
		{
			name:      "invalid GUID",
			mutate:    func(m *domain.MachineObservation) { m.ID = "invalid-guid" },
			wantErrIs: domain.ErrInvalidMachineID,
		},
		{
			name:      "invalid Name",
			mutate:    func(m *domain.MachineObservation) { m.Name = "" },
			wantErrIs: domain.ErrInvalidMachineName,
		},
		{
			name:      "invalid State",
			mutate:    func(m *domain.MachineObservation) { m.State = domain.MachineLifecycleState("bogus") },
			wantErrIs: domain.ErrInvalidLifecycleState,
		},
		{
			name:      "negative generation",
			mutate:    func(m *domain.MachineObservation) { m.Generation = -1 },
			wantErrIs: domain.ErrInvalidMetricValue,
		},
		{
			name:      "generation 0",
			mutate:    func(m *domain.MachineObservation) { m.Generation = 0 },
			wantErrIs: domain.ErrInvalidMetricValue,
		},
		{
			name:      "generation 3",
			mutate:    func(m *domain.MachineObservation) { m.Generation = 3 },
			wantErrIs: domain.ErrInvalidMetricValue,
		},
		{
			name:      "empty version",
			mutate:    func(m *domain.MachineObservation) { m.Version = "" },
			wantErrIs: domain.ErrInvalidMachineName,
		},
		{
			name:      "negative uptime",
			mutate:    func(m *domain.MachineObservation) { m.UptimeMs = -10 },
			wantErrIs: domain.ErrInvalidMetricValue,
		},
		{
			name:      "negative cpu usage",
			mutate:    func(m *domain.MachineObservation) { m.CPUUsagePercent = -5 },
			wantErrIs: domain.ErrInvalidMetricValue,
		},
		{
			name:      "cpu usage over 100",
			mutate:    func(m *domain.MachineObservation) { m.CPUUsagePercent = 101 },
			wantErrIs: domain.ErrInvalidMetricValue,
		},
		{
			name:      "zero timestamp",
			mutate:    func(m *domain.MachineObservation) { m.ObservedAt = time.Time{} },
			wantErrIs: domain.ErrInvalidObservationTimestamp,
		},
		{
			name:      "invalid observation type",
			mutate:    func(m *domain.MachineObservation) { m.ObservationType = domain.ObservationType("inferred") },
			wantErrIs: domain.ErrInvalidObservationType,
		},
		{
			name: "invalid network adapter name",
			mutate: func(m *domain.MachineObservation) {
				m.NetworkAdapters = []domain.NetworkAdapterSummary{{Name: " bad adapter with leading space "}}
			},
			wantErrIs: domain.ErrInvalidNetworkAdapter,
		},
		{
			name: "invalid network adapter MAC format",
			mutate: func(m *domain.MachineObservation) {
				m.NetworkAdapters = []domain.NetworkAdapterSummary{{
					Name:       "eth0",
					MACAddress: "invalid-mac-address",
				}}
			},
			wantErrIs: domain.ErrInvalidNetworkAdapter,
		},
		{
			name: "invalid network adapter IP address",
			mutate: func(m *domain.MachineObservation) {
				m.NetworkAdapters = []domain.NetworkAdapterSummary{{
					Name:        "eth0",
					IPAddresses: []string{"999.999.999.999"},
				}}
			},
			wantErrIs: domain.ErrInvalidNetworkAdapter,
		},
		{
			name:      "invalid version",
			mutate:    func(m *domain.MachineObservation) { m.Version = strings.Repeat("v", 100) },
			wantErrIs: domain.ErrInvalidMachineName,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := base
			tt.mutate(&m)
			if err := m.Validate(); !errors.Is(err, tt.wantErrIs) {
				t.Errorf("expected %v, got %v", tt.wantErrIs, err)
			}
		})
	}
}

func TestValidateMACAddress(t *testing.T) {
	valid := []string{
		"",
		"00155D010203",
		"00:15:5D:01:02:03",
		"00-15-5D-01-02-03",
		"aabbccddeeff",
		"AA:BB:CC:DD:EE:FF",
	}
	for _, m := range valid {
		if err := domain.ValidateMACAddress(m); err != nil {
			t.Errorf("expected valid MAC %q, got %v", m, err)
		}
	}

	invalid := []string{
		"123",
		"00155D01020G",
		"00:15:5D:01:02:0",
		"00:15:5D:01:02:03:04",
		"not-a-mac",
	}
	for _, m := range invalid {
		if err := domain.ValidateMACAddress(m); err == nil {
			t.Errorf("expected error for invalid MAC %q", m)
		}
	}
}

func TestMachineObservation_Clone(t *testing.T) {
	orig := makeValidObservation()
	clone := orig.Clone()

	if clone.ID != orig.ID || clone.Name != orig.Name {
		t.Errorf("clone basic fields mismatch")
	}

	// Mutating clone should not mutate original
	clone.NetworkAdapters[0].IPAddresses[0] = "10.0.0.99"
	if orig.NetworkAdapters[0].IPAddresses[0] == "10.0.0.99" {
		t.Errorf("mutating clone mutated original IPAddresses")
	}
}
