package domain

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

const (
	// LocalHostID is the stable synthetic ID reserved for the automatic local Hyper-V route.
	LocalHostID HostID = "local"

	maxHostIDLength       = 128
	maxHostAddressLength  = 512
	maxExactAliasLength   = 128
	canonicalLocatorDelim = ":"
)

var (
	ErrInvalidHostID          = errors.New("domain: invalid host ID")
	ErrInvalidHostAddress     = errors.New("domain: invalid host address")
	ErrInvalidAlias           = errors.New("domain: invalid exact alias")
	ErrInvalidMachineLocator  = errors.New("domain: invalid canonical machine locator")
	ErrMachineReferenceEmpty  = errors.New("domain: empty machine reference")
	ErrMachineReferenceMiss   = errors.New("domain: machine reference not found")
	ErrMachineReferenceAmbig  = errors.New("domain: machine reference is ambiguous")
	ErrMachineReferenceStale  = errors.New("domain: machine reference is stale")
	ErrMachineHostDisabled    = errors.New("domain: machine host is disabled")
	ErrMachineHostUnavailable = errors.New("domain: machine host is unavailable")
	ErrMachineAccessDenied    = errors.New("domain: machine host access denied")
)

// HostID is a stable opaque host identifier owned by local operator state.
type HostID string

// NewHostID validates and normalizes an opaque host ID.
func NewHostID(value string) (HostID, error) {
	cleaned := strings.TrimSpace(value)
	if cleaned == "" || len(cleaned) > maxHostIDLength {
		return "", fmt.Errorf("%w: expected 1-%d characters", ErrInvalidHostID, maxHostIDLength)
	}
	for _, r := range cleaned {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' {
			continue
		}
		return "", fmt.Errorf("%w: unsupported character %q", ErrInvalidHostID, r)
	}
	return HostID(cleaned), nil
}

// String returns the host ID as a string.
func (id HostID) String() string {
	return string(id)
}

// Validate checks that the host ID is syntactically valid.
func (id HostID) Validate() error {
	_, err := NewHostID(string(id))
	return err
}

// MachineLocator is the canonical operation identity for a Hyper-V VM.
type MachineLocator struct {
	HostID HostID
	VMID   string
}

// NewMachineLocator validates and normalizes a canonical machine locator.
func NewMachineLocator(hostID HostID, vmid string) (MachineLocator, error) {
	if err := hostID.Validate(); err != nil {
		return MachineLocator{}, err
	}
	normalizedVMID, err := NormalizeMachineGUID(vmid)
	if err != nil {
		return MachineLocator{}, err
	}
	return MachineLocator{HostID: hostID, VMID: normalizedVMID}, nil
}

// ParseMachineLocator parses the stable string form "host-id:vm-guid".
func ParseMachineLocator(value string) (MachineLocator, error) {
	host, vmid, ok := strings.Cut(strings.TrimSpace(value), canonicalLocatorDelim)
	if !ok || host == "" || vmid == "" {
		return MachineLocator{}, ErrInvalidMachineLocator
	}
	locator, err := NewMachineLocator(HostID(host), vmid)
	if err != nil {
		return MachineLocator{}, fmt.Errorf("%w: %v", ErrInvalidMachineLocator, err)
	}
	return locator, nil
}

// String returns the canonical stable string representation.
func (m MachineLocator) String() string {
	return m.HostID.String() + canonicalLocatorDelim + m.VMID
}

// Validate checks all locator invariants.
func (m MachineLocator) Validate() error {
	_, err := NewMachineLocator(m.HostID, m.VMID)
	return err
}

// NormalizeMachineGUID validates and lower-cases a Hyper-V VM GUID.
func NormalizeMachineGUID(id string) (string, error) {
	cleaned := strings.ToLower(strings.TrimSpace(id))
	if err := ValidateMachineGUID(cleaned); err != nil {
		return "", err
	}
	return cleaned, nil
}

// ValidateHostAddress checks an operator-supplied host address without resolving it.
func ValidateHostAddress(address string) error {
	cleaned := strings.TrimSpace(address)
	if cleaned == "" || len(cleaned) > maxHostAddressLength {
		return fmt.Errorf("%w: expected 1-%d characters", ErrInvalidHostAddress, maxHostAddressLength)
	}
	for _, r := range cleaned {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return fmt.Errorf("%w: contains whitespace or control character", ErrInvalidHostAddress)
		}
	}
	return nil
}

// NormalizeExactAlias validates an exact host or machine alias.
func NormalizeExactAlias(alias string) (string, error) {
	cleaned := strings.TrimSpace(alias)
	if cleaned == "" || len(cleaned) > maxExactAliasLength {
		return "", fmt.Errorf("%w: expected 1-%d characters", ErrInvalidAlias, maxExactAliasLength)
	}
	for _, r := range cleaned {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: contains control character", ErrInvalidAlias)
		}
	}
	return cleaned, nil
}
