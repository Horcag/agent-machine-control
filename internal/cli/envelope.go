package cli

import (
	"sort"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

// SchemaVersion defines the canonical CLI JSON envelope version.
const SchemaVersion = "1"

// DoctorOutputEnvelope defines the structured JSON output for `amc doctor --json`.
type DoctorOutputEnvelope struct {
	SchemaVersion string           `json:"schema_version"`
	Status        app.DoctorStatus `json:"status"`
	Ready         bool             `json:"ready"`
	Reason        app.DoctorReason `json:"reason,omitempty"`
	Message       string           `json:"message"`
	Capabilities  []string         `json:"capabilities"`
	ObservedAt    string           `json:"observed_at"`
}

// NetworkAdapterOutputDTO defines the structured JSON output for a network adapter.
type NetworkAdapterOutputDTO struct {
	Name        string   `json:"name"`
	SwitchName  string   `json:"switch_name,omitempty"`
	MACAddress  string   `json:"mac_address,omitempty"`
	IPAddresses []string `json:"ip_addresses"`
	Status      string   `json:"status,omitempty"`
}

// MachineOutputDTO defines the deterministic public JSON representation of a machine observation.
type MachineOutputDTO struct {
	ID                  string                       `json:"id"`
	Name                string                       `json:"name"`
	State               domain.MachineLifecycleState `json:"state"`
	RawState            string                       `json:"raw_state"`
	RawStatus           string                       `json:"raw_status,omitempty"`
	Generation          int                          `json:"generation"`
	Version             string                       `json:"version"`
	UptimeMs            int64                        `json:"uptime_ms"`
	CPUUsagePercent     int                          `json:"cpu_usage_percent"`
	MemoryAssignedBytes uint64                       `json:"memory_assigned_bytes"`
	NetworkAdapters     []NetworkAdapterOutputDTO    `json:"network_adapters"`
	Capabilities        []string                     `json:"capabilities"`
	ObservedAt          string                       `json:"observed_at"`
	ObservationType     domain.ObservationType       `json:"observation_type"`
}

// MachineListOutputEnvelope defines the structured JSON output for `amc machine list --json`.
type MachineListOutputEnvelope struct {
	SchemaVersion   string                 `json:"schema_version"`
	ObservationType domain.ObservationType `json:"observation_type"`
	Machines        []MachineOutputDTO     `json:"machines"`
}

// MachineInspectOutputEnvelope defines the structured JSON output for `amc machine inspect <id> --json`.
type MachineInspectOutputEnvelope struct {
	SchemaVersion   string                 `json:"schema_version"`
	ObservationType domain.ObservationType `json:"observation_type"`
	Machine         MachineOutputDTO       `json:"machine"`
}

// ConvertToMachineDTO converts a domain MachineObservation into a deterministic MachineOutputDTO.
func ConvertToMachineDTO(m domain.MachineObservation) MachineOutputDTO {
	caps := m.Capabilities.Slice()
	if caps == nil {
		caps = []string{}
	}

	adapters := []NetworkAdapterOutputDTO{}
	if len(m.NetworkAdapters) > 0 {
		adapters = make([]NetworkAdapterOutputDTO, len(m.NetworkAdapters))
		for i, na := range m.NetworkAdapters {
			ips := []string{}
			if len(na.IPAddresses) > 0 {
				ips = make([]string, len(na.IPAddresses))
				copy(ips, na.IPAddresses)
				sort.Strings(ips)
			}
			adapters[i] = NetworkAdapterOutputDTO{
				Name:        na.Name,
				SwitchName:  na.SwitchName,
				MACAddress:  na.MACAddress,
				IPAddresses: ips,
				Status:      na.Status,
			}
		}
		sort.Slice(adapters, func(i, j int) bool {
			if adapters[i].Name == adapters[j].Name {
				return adapters[i].MACAddress < adapters[j].MACAddress
			}
			return adapters[i].Name < adapters[j].Name
		})
	}

	return MachineOutputDTO{
		ID:                  m.ID,
		Name:                m.Name,
		State:               m.State,
		RawState:            m.RawState,
		RawStatus:           m.RawStatus,
		Generation:          m.Generation,
		Version:             m.Version,
		UptimeMs:            m.UptimeMs,
		CPUUsagePercent:     m.CPUUsagePercent,
		MemoryAssignedBytes: m.MemoryAssignedBytes,
		NetworkAdapters:     adapters,
		Capabilities:        caps,
		ObservedAt:          m.ObservedAt.UTC().Format(time.RFC3339),
		ObservationType:     m.ObservationType,
	}
}
