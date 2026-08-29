package domain_test

import (
	"reflect"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestReadOnlyMachineCapabilities(t *testing.T) {
	caps := domain.ReadOnlyMachineCapabilities()

	if err := caps.Validate(); err != nil {
		t.Fatalf("ReadOnlyMachineCapabilities set failed validation: %v", err)
	}

	if len(caps) != 4 {
		t.Fatalf("expected exactly 4 capabilities, got %d", len(caps))
	}

	expected := []string{
		domain.CapabilityHostDiagnostics,
		domain.CapabilityMachineInspect,
		domain.CapabilityMachineList,
		domain.CapabilityNetworkAdapterObserve,
	}

	for _, exp := range expected {
		if !caps.Has(exp) {
			t.Errorf("expected capability %q in set", exp)
		}
	}

	slice := caps.Slice()
	if !reflect.DeepEqual(slice, expected) {
		t.Errorf("expected sorted slice %v, got %v", expected, slice)
	}
}

func TestDirectMachineCapabilities(t *testing.T) {
	caps := domain.DirectMachineCapabilities()

	if err := caps.Validate(); err != nil {
		t.Fatalf("DirectMachineCapabilities set failed validation: %v", err)
	}

	if len(caps) != 9 {
		t.Fatalf("expected exactly 9 capabilities, got %d", len(caps))
	}

	expected := []string{
		domain.CapabilityCheckpointCreate,
		domain.CapabilityCheckpointList,
		domain.CapabilityCheckpointRestore,
		domain.CapabilityHostDiagnostics,
		domain.CapabilityMachineInspect,
		domain.CapabilityMachineList,
		domain.CapabilityMachineStart,
		domain.CapabilityMachineStop,
		domain.CapabilityNetworkAdapterObserve,
	}

	for _, exp := range expected {
		if !caps.Has(exp) {
			t.Errorf("expected capability %q in set", exp)
		}
	}
}
