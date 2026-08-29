package hyperv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

type rawMachine struct {
	ID                  string          `json:"id"`
	Name                string          `json:"name"`
	State               string          `json:"state"`
	Status              string          `json:"status,omitempty"`
	Generation          int             `json:"generation"`
	Version             string          `json:"version"`
	UptimeMs            int64           `json:"uptime_ms"`
	CPUUsage            int             `json:"cpu_usage"`
	MemoryAssignedBytes int64           `json:"memory_assigned_bytes"`
	NetworkAdapters     json.RawMessage `json:"network_adapters"`
}

type rawNetworkAdapter struct {
	Name        string          `json:"name"`
	SwitchName  string          `json:"switch_name,omitempty"`
	MACAddress  string          `json:"mac_address,omitempty"`
	IPAddresses json.RawMessage `json:"ip_addresses,omitempty"`
	Status      string          `json:"status,omitempty"`
}

func normalizeMachineList(raw json.RawMessage) ([]rawMachine, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("%w: empty machines payload", ErrMalformedResponse)
	}
	if string(trimmed) == "null" {
		return nil, fmt.Errorf("%w: null machines payload", ErrMalformedResponse)
	}
	if trimmed[0] == '[' {
		var rawList []json.RawMessage
		if err := decodeStrictJSON(trimmed, &rawList); err != nil {
			return nil, fmt.Errorf("%w: failed to decode machine array: %w", ErrMalformedResponse, err)
		}
		list := make([]rawMachine, len(rawList))
		for i, item := range rawList {
			if err := decodeStrictJSON(item, &list[i]); err != nil {
				return nil, fmt.Errorf("%w: invalid machine item: %w", ErrMalformedResponse, err)
			}
		}
		return list, nil
	}
	if trimmed[0] == '{' {
		var single rawMachine
		if err := decodeStrictJSON(trimmed, &single); err != nil {
			return nil, fmt.Errorf("%w: failed to decode single machine: %w", ErrMalformedResponse, err)
		}
		return []rawMachine{single}, nil
	}
	return nil, fmt.Errorf("%w: expected JSON array or object for machines", ErrMalformedResponse)
}

func convertRawMachine(raw rawMachine, now time.Time) (domain.MachineObservation, error) {
	if raw.MemoryAssignedBytes < 0 {
		return domain.MachineObservation{}, fmt.Errorf("%w: memory %d is negative", domain.ErrInvalidMetricValue, raw.MemoryAssignedBytes)
	}

	adapters, err := normalizeNetworkAdapters(raw.NetworkAdapters)
	if err != nil {
		return domain.MachineObservation{}, err
	}

	obs := domain.MachineObservation{
		ID:                  raw.ID,
		Name:                raw.Name,
		State:               domain.NormalizeLifecycleState(raw.State),
		RawState:            raw.State,
		RawStatus:           raw.Status,
		Generation:          raw.Generation,
		Version:             raw.Version,
		UptimeMs:            raw.UptimeMs,
		CPUUsagePercent:     raw.CPUUsage,
		MemoryAssignedBytes: uint64(raw.MemoryAssignedBytes),
		NetworkAdapters:     adapters,
		Capabilities:        domain.ReadOnlyMachineCapabilities(),
		ObservedAt:          now.UTC(),
		ObservationType:     domain.ObservationObserved,
	}

	if err := obs.Validate(); err != nil {
		return domain.MachineObservation{}, err
	}
	return obs, nil
}

func normalizeNetworkAdapters(raw json.RawMessage) ([]domain.NetworkAdapterSummary, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil
	}

	var rawAdapters []rawNetworkAdapter
	switch trimmed[0] {
	case '[':
		var rawList []json.RawMessage
		if err := decodeStrictJSON(trimmed, &rawList); err != nil {
			return nil, fmt.Errorf("%w: invalid network adapters array: %w", ErrMalformedResponse, err)
		}
		rawAdapters = make([]rawNetworkAdapter, len(rawList))
		for i, item := range rawList {
			if err := decodeStrictJSON(item, &rawAdapters[i]); err != nil {
				return nil, fmt.Errorf("%w: invalid network adapter item: %w", ErrMalformedResponse, err)
			}
		}
	case '{':
		var single rawNetworkAdapter
		if err := decodeStrictJSON(trimmed, &single); err != nil {
			return nil, fmt.Errorf("%w: invalid network adapter object: %w", ErrMalformedResponse, err)
		}
		rawAdapters = []rawNetworkAdapter{single}
	default:
		return nil, fmt.Errorf("%w: unexpected format for network adapters", ErrMalformedResponse)
	}

	summaries := make([]domain.NetworkAdapterSummary, len(rawAdapters))
	for i, ra := range rawAdapters {
		ips, err := normalizeIPAddresses(ra.IPAddresses)
		if err != nil {
			return nil, err
		}
		summary := domain.NetworkAdapterSummary{
			Name:        ra.Name,
			SwitchName:  ra.SwitchName,
			MACAddress:  ra.MACAddress,
			IPAddresses: ips,
			Status:      ra.Status,
		}
		if err := summary.Validate(); err != nil {
			return nil, err
		}
		summaries[i] = summary
	}
	return summaries, nil
}

func normalizeIPAddresses(raw json.RawMessage) ([]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil
	}
	if trimmed[0] == '[' {
		var ips []string
		if err := decodeStrictJSON(trimmed, &ips); err != nil {
			return nil, fmt.Errorf("%w: invalid ip address array: %w", ErrMalformedResponse, err)
		}
		return ips, nil
	}
	if trimmed[0] == '"' {
		var single string
		if err := decodeStrictJSON(trimmed, &single); err != nil {
			return nil, fmt.Errorf("%w: invalid ip address string: %w", ErrMalformedResponse, err)
		}
		if single == "" {
			return nil, nil
		}
		return []string{single}, nil
	}
	return nil, fmt.Errorf("%w: unexpected format for IP addresses", ErrMalformedResponse)
}
