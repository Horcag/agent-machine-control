package domain

import (
	"fmt"
	"net"
	"strings"
	"time"
)

// MachineLifecycleState defines the normalized lifecycle state of a virtual machine.
type MachineLifecycleState string

const (
	MachineStateRunning  MachineLifecycleState = "running"
	MachineStateOff      MachineLifecycleState = "off"
	MachineStatePaused   MachineLifecycleState = "paused"
	MachineStateSaved    MachineLifecycleState = "saved"
	MachineStateStarting MachineLifecycleState = "starting"
	MachineStateStopping MachineLifecycleState = "stopping"
	MachineStateSaving   MachineLifecycleState = "saving"
	MachineStatePausing  MachineLifecycleState = "pausing"
	MachineStateResuming MachineLifecycleState = "resuming"
	MachineStateUnknown  MachineLifecycleState = "unknown"
)

// IsValid returns true if the lifecycle state is a recognized normalized state.
func (s MachineLifecycleState) IsValid() bool {
	switch s {
	case MachineStateRunning, MachineStateOff, MachineStatePaused, MachineStateSaved,
		MachineStateStarting, MachineStateStopping, MachineStateSaving, MachineStatePausing,
		MachineStateResuming, MachineStateUnknown:
		return true
	default:
		return false
	}
}

// String returns the string representation of the lifecycle state.
func (s MachineLifecycleState) String() string {
	return string(s)
}

// NormalizeLifecycleState maps provider-specific raw state strings to normalized lifecycle states.
// Any unknown provider state is safely mapped to MachineStateUnknown while the caller preserves the raw string.
func NormalizeLifecycleState(raw string) MachineLifecycleState {
	cleaned := strings.ToLower(strings.TrimSpace(raw))
	switch cleaned {
	case "running":
		return MachineStateRunning
	case "off", "poweredoff", "poweroff":
		return MachineStateOff
	case "paused":
		return MachineStatePaused
	case "saved":
		return MachineStateSaved
	case "starting":
		return MachineStateStarting
	case "stopping":
		return MachineStateStopping
	case "saving":
		return MachineStateSaving
	case "pausing":
		return MachineStatePausing
	case "resuming":
		return MachineStateResuming
	default:
		return MachineStateUnknown
	}
}

// ValidateMachineGUID validates that an ID is a well-formed Hyper-V VM GUID.
func ValidateMachineGUID(id string) error {
	if len(id) != 36 {
		return fmt.Errorf("%w: expected 36-character GUID, got length %d", ErrInvalidMachineID, len(id))
	}
	for i := range 36 {
		c := id[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return fmt.Errorf("%w: expected hyphen at index %d in GUID %q", ErrInvalidMachineID, i, id)
			}
		} else {
			isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
			if !isHex {
				return fmt.Errorf("%w: invalid hex character %q in GUID %q", ErrInvalidMachineID, c, id)
			}
		}
	}
	return nil
}

// ValidateMACAddress validates the syntactic format of an observed MAC address.
func ValidateMACAddress(mac string) error {
	if mac == "" {
		return nil
	}
	cleaned := strings.ReplaceAll(strings.ReplaceAll(mac, ":", ""), "-", "")
	if len(cleaned) != 12 {
		return fmt.Errorf("%w: invalid MAC address format %q", ErrInvalidNetworkAdapter, mac)
	}
	for i := range len(cleaned) {
		c := cleaned[i]
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return fmt.Errorf("%w: invalid hex character in MAC address %q", ErrInvalidNetworkAdapter, mac)
		}
	}
	return nil
}

// NetworkAdapterSummary summarizes a network adapter observed on a virtual machine.
type NetworkAdapterSummary struct {
	Name        string
	SwitchName  string
	MACAddress  string
	IPAddresses []string
	Status      string
}

// Validate checks that the NetworkAdapterSummary fields satisfy domain invariants.
func (na NetworkAdapterSummary) Validate() error {
	if err := ValidateBoundedString(na.Name, 1, 256, ErrInvalidNetworkAdapter); err != nil {
		return err
	}
	if na.SwitchName != "" {
		if err := ValidateBoundedString(na.SwitchName, 1, 256, ErrInvalidNetworkAdapter); err != nil {
			return err
		}
	}
	if na.MACAddress != "" {
		if err := ValidateBoundedString(na.MACAddress, 1, 64, ErrInvalidNetworkAdapter); err != nil {
			return err
		}
		if err := ValidateMACAddress(na.MACAddress); err != nil {
			return err
		}
	}
	if na.Status != "" {
		if err := ValidateBoundedString(na.Status, 1, 256, ErrInvalidNetworkAdapter); err != nil {
			return err
		}
	}
	for _, ip := range na.IPAddresses {
		if err := ValidateBoundedString(ip, 1, 128, ErrInvalidNetworkAdapter); err != nil {
			return err
		}
		if net.ParseIP(ip) == nil {
			return fmt.Errorf("%w: invalid IP address format %q", ErrInvalidNetworkAdapter, ip)
		}
	}
	return nil
}

// MachineObservation represents a verified, point-in-time observation of a virtual machine.
type MachineObservation struct {
	HostID              HostID
	Locator             MachineLocator
	ID                  string
	Name                string
	State               MachineLifecycleState
	RawState            string
	RawStatus           string
	Generation          int
	Version             string
	UptimeMs            int64
	CPUUsagePercent     int
	MemoryAssignedBytes uint64
	NetworkAdapters     []NetworkAdapterSummary
	Capabilities        CapabilitySet
	ObservedAt          time.Time
	ObservationType     ObservationType
}

// Clone returns a deep copy of the MachineObservation.
func (m MachineObservation) Clone() MachineObservation {
	clone := m
	if m.Capabilities != nil {
		clone.Capabilities = m.Capabilities.Clone()
	}
	if m.NetworkAdapters != nil {
		clone.NetworkAdapters = make([]NetworkAdapterSummary, len(m.NetworkAdapters))
		for i, na := range m.NetworkAdapters {
			naClone := na
			if na.IPAddresses != nil {
				naClone.IPAddresses = make([]string, len(na.IPAddresses))
				copy(naClone.IPAddresses, na.IPAddresses)
			}
			clone.NetworkAdapters[i] = naClone
		}
	}
	return clone
}

// Validate checks all domain invariants for the MachineObservation.
func (m MachineObservation) Validate() error {
	if err := m.validateIdentityAndState(); err != nil {
		return err
	}
	if err := m.validateMetrics(); err != nil {
		return err
	}
	return m.validateObservationMetadata()
}

func (m MachineObservation) validateIdentityAndState() error {
	normalizedID, err := NormalizeMachineGUID(m.ID)
	if err != nil {
		return err
	}
	if m.HostID != "" {
		if err := m.HostID.Validate(); err != nil {
			return err
		}
	}
	if m.Locator != (MachineLocator{}) {
		if err := m.Locator.Validate(); err != nil {
			return err
		}
		if m.HostID != "" && m.Locator.HostID != m.HostID {
			return fmt.Errorf("%w: locator host %s does not match observation host %s", ErrInvalidMachineLocator, m.Locator.HostID, m.HostID)
		}
		if m.Locator.VMID != normalizedID {
			return fmt.Errorf("%w: locator VM %s does not match observation ID %s", ErrInvalidMachineLocator, m.Locator.VMID, normalizedID)
		}
	}
	if err := ValidateBoundedString(m.Name, 1, 256, ErrInvalidMachineName); err != nil {
		return err
	}
	if !m.State.IsValid() {
		return fmt.Errorf("%w: unrecognized state %q", ErrInvalidLifecycleState, m.State)
	}
	if err := ValidateBoundedString(m.RawState, 1, 256, ErrInvalidLifecycleState); err != nil {
		return err
	}
	if m.RawStatus != "" {
		if err := ValidateBoundedString(m.RawStatus, 1, 512, ErrInvalidLifecycleState); err != nil {
			return err
		}
	}
	return nil
}

func (m MachineObservation) validateMetrics() error {
	if m.Generation != 1 && m.Generation != 2 {
		return fmt.Errorf("%w: generation %d must be 1 or 2", ErrInvalidMetricValue, m.Generation)
	}
	if err := ValidateBoundedString(m.Version, 1, 64, ErrInvalidMachineName); err != nil {
		return err
	}
	if m.UptimeMs < 0 {
		return fmt.Errorf("%w: uptime_ms %d cannot be negative", ErrInvalidMetricValue, m.UptimeMs)
	}
	if m.CPUUsagePercent < 0 || m.CPUUsagePercent > 100 {
		return fmt.Errorf("%w: cpu_usage_percent %d out of range [0, 100]", ErrInvalidMetricValue, m.CPUUsagePercent)
	}
	return nil
}

func (m MachineObservation) validateObservationMetadata() error {
	if m.ObservedAt.IsZero() {
		return ErrInvalidObservationTimestamp
	}
	if m.ObservationType != ObservationObserved {
		return fmt.Errorf("%w: expected %s, got %s", ErrInvalidObservationType, ObservationObserved, m.ObservationType)
	}
	if err := m.Capabilities.Validate(); err != nil {
		return err
	}
	for _, na := range m.NetworkAdapters {
		if err := na.Validate(); err != nil {
			return err
		}
	}
	return nil
}
