package domain_test

import (
	"errors"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestMachineLocatorStableStringAndParse(t *testing.T) {
	locator, err := domain.NewMachineLocator("host-a", "C4A523D4-6B99-4D62-A5E2-4752C0F20001")
	if err != nil {
		t.Fatalf("NewMachineLocator failed: %v", err)
	}
	if locator.String() != "host-a:c4a523d4-6b99-4d62-a5e2-4752c0f20001" {
		t.Fatalf("unexpected locator string %q", locator.String())
	}
	parsed, err := domain.ParseMachineLocator(locator.String())
	if err != nil {
		t.Fatalf("ParseMachineLocator failed: %v", err)
	}
	if parsed != locator {
		t.Fatalf("parsed locator mismatch: got %+v want %+v", parsed, locator)
	}
}

func TestHostAuthorityRejectsNonCanonicalWhitespace(t *testing.T) {
	guid := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	for _, value := range []string{" host-a ", " local "} {
		t.Run("host ID "+value, func(t *testing.T) {
			if _, err := domain.NewHostID(value); !errors.Is(err, domain.ErrInvalidHostID) {
				t.Fatalf("NewHostID(%q) expected ErrInvalidHostID, got %v", value, err)
			}
			if err := domain.HostID(value).Validate(); !errors.Is(err, domain.ErrInvalidHostID) {
				t.Fatalf("HostID(%q).Validate expected ErrInvalidHostID, got %v", value, err)
			}
			if _, err := domain.NewMachineLocator(domain.HostID(value), guid); !errors.Is(err, domain.ErrInvalidHostID) {
				t.Fatalf("NewMachineLocator(%q) expected ErrInvalidHostID, got %v", value, err)
			}
		})
	}

	for _, value := range []string{
		" host-a:" + guid,
		"host-a:" + guid + " ",
	} {
		if _, err := domain.ParseMachineLocator(value); !errors.Is(err, domain.ErrInvalidMachineLocator) {
			t.Fatalf("ParseMachineLocator(%q) expected ErrInvalidMachineLocator, got %v", value, err)
		}
	}

	for _, address := range []string{" trusted-host.example", "trusted-host.example "} {
		if err := domain.ValidateHostAddress(address); !errors.Is(err, domain.ErrInvalidHostAddress) {
			t.Fatalf("ValidateHostAddress(%q) expected ErrInvalidHostAddress, got %v", address, err)
		}
	}
}

func TestHostIDAndAliasValidation(t *testing.T) {
	if _, err := domain.NewHostID("host one"); !errors.Is(err, domain.ErrInvalidHostID) {
		t.Fatalf("expected ErrInvalidHostID for whitespace host ID, got %v", err)
	}
	if err := domain.ValidateHostAddress("trusted-host.example"); err != nil {
		t.Fatalf("expected trusted host address to validate, got %v", err)
	}
	if err := domain.ValidateHostAddress("trusted host"); !errors.Is(err, domain.ErrInvalidHostAddress) {
		t.Fatalf("expected ErrInvalidHostAddress for whitespace address, got %v", err)
	}
	if _, err := domain.NormalizeExactAlias("alias-a"); err != nil {
		t.Fatalf("expected exact alias to validate, got %v", err)
	}
	if _, err := domain.NormalizeExactAlias("\n"); !errors.Is(err, domain.ErrInvalidAlias) {
		t.Fatalf("expected ErrInvalidAlias for empty alias, got %v", err)
	}
}

func TestMachineObservationOptionalLocatorValidation(t *testing.T) {
	obs := makeValidObservation()
	obs.HostID = "host-a"
	obs.Locator = domain.MachineLocator{HostID: "host-a", VMID: obs.ID}
	if err := obs.Validate(); err != nil {
		t.Fatalf("expected observation with matching locator to validate, got %v", err)
	}
	obs.Locator = domain.MachineLocator{HostID: "host-b", VMID: obs.ID}
	if err := obs.Validate(); !errors.Is(err, domain.ErrInvalidMachineLocator) {
		t.Fatalf("expected ErrInvalidMachineLocator for host mismatch, got %v", err)
	}
}
