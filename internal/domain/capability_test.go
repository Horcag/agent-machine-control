package domain_test

import (
	"errors"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestCapability_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cap     domain.Capability
		wantErr bool
	}{
		{
			name:    "valid capability",
			cap:     domain.Capability("hyperv.lifecycle"),
			wantErr: false,
		},
		{
			name:    "empty capability",
			cap:     domain.Capability(""),
			wantErr: true,
		},
		{
			name:    "capability with leading space",
			cap:     domain.Capability(" hyperv.lifecycle"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cap.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Capability.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && !errors.Is(err, domain.ErrInvalidCapability) {
				t.Errorf("expected ErrInvalidCapability, got %v", err)
			}
		})
	}
}

func TestCapability_String(t *testing.T) {
	c := domain.Capability("hyperv.framebuffer")
	if c.String() != "hyperv.framebuffer" {
		t.Errorf("Capability.String() = %q, want %q", c.String(), "hyperv.framebuffer")
	}
}

func TestCapabilitySet_Basic(t *testing.T) {
	cs := domain.NewCapabilitySet("hyperv.lifecycle", "hyperv.inspect")
	if !cs.Has("hyperv.lifecycle") {
		t.Errorf("expected hyperv.lifecycle to be present")
	}
	if !cs.Has("hyperv.inspect") {
		t.Errorf("expected hyperv.inspect to be present")
	}
	if cs.Has("hyperv.framebuffer") {
		t.Errorf("expected hyperv.framebuffer to be absent")
	}

	slice := cs.Slice()
	if len(slice) != 2 || slice[0] != "hyperv.inspect" || slice[1] != "hyperv.lifecycle" {
		t.Errorf("CapabilitySet.Slice() unexpected: %v", slice)
	}

	var nilSet domain.CapabilitySet
	if nilSet.Has("any") {
		t.Errorf("nil CapabilitySet.Has should return false")
	}
	if nilSet.Slice() != nil {
		t.Errorf("nil CapabilitySet.Slice should return nil")
	}
	if nilSet.Clone() == nil {
		t.Errorf("nil CapabilitySet.Clone should return non-nil empty set")
	}

	sub := domain.NewCapabilitySet("hyperv.inspect")
	if !sub.IsSubsetOf(cs) {
		t.Errorf("expected sub to be subset of cs")
	}
	if cs.IsSubsetOf(sub) {
		t.Errorf("expected cs to not be subset of sub")
	}

	cloned := cs.Clone()
	cs["hyperv.framebuffer"] = struct{}{}
	if cloned.Has("hyperv.framebuffer") {
		t.Errorf("cloned set mutated when original map was modified")
	}

	invalidSet := domain.NewCapabilitySet(" bad.cap")
	if err := invalidSet.Validate(); err == nil || !errors.Is(err, domain.ErrInvalidCapability) {
		t.Errorf("expected ErrInvalidCapability for invalid capability in set, got %v", err)
	}
}
