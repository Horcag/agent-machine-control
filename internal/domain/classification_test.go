package domain_test

import (
	"errors"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestOperationClass_ParseValid(t *testing.T) {
	tests := []struct {
		input           string
		wantClass       domain.OperationClass
		wantMutation    bool
		wantApprovalReq bool
	}{
		{"observe", domain.ClassObserve, false, false},
		{"reversible_mutation", domain.ClassReversibleMutation, true, false},
		{"destructive_privileged", domain.ClassDestructivePrivileged, true, true},
		{"forbidden", domain.ClassForbidden, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			class, err := domain.ParseOperationClass(tt.input)
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tt.input, err)
			}
			if class != tt.wantClass {
				t.Errorf("got %v, want %v", class, tt.wantClass)
			}
			if !class.IsValid() {
				t.Errorf("expected %v to be valid", class)
			}
			if class.IsMutation() != tt.wantMutation {
				t.Errorf("IsMutation() = %v, want %v", class.IsMutation(), tt.wantMutation)
			}
			if class.RequiresApproval() != tt.wantApprovalReq {
				t.Errorf("RequiresApproval() = %v, want %v", class.RequiresApproval(), tt.wantApprovalReq)
			}
			if class.String() != tt.input {
				t.Errorf("String() = %q, want %q", class.String(), tt.input)
			}
		})
	}
}

func TestOperationClass_ParseInvalid(t *testing.T) {
	invalidInputs := []string{"", "unknown_class", "   ", "OBSERVE"}
	for _, in := range invalidInputs {
		t.Run(in, func(t *testing.T) {
			class, err := domain.ParseOperationClass(in)
			if err == nil {
				t.Fatalf("expected error for invalid class %q, got class %v", in, class)
			}
			if !errors.Is(err, domain.ErrInvalidOperationClass) {
				t.Errorf("expected ErrInvalidOperationClass, got %v", err)
			}
			if class.IsValid() {
				t.Errorf("expected invalid class to report IsValid() == false")
			}
		})
	}
}

func TestEvidenceSensitivity(t *testing.T) {
	tests := []struct {
		val          domain.EvidenceSensitivity
		wantValid    bool
		wantSens     bool
		wantStrMatch string
	}{
		{domain.EvidenceSensitivityUnspecified, true, false, ""},
		{domain.EvidenceSensitivityStandard, true, false, "standard"},
		{domain.EvidenceSensitivitySensitive, true, true, "sensitive"},
		{domain.EvidenceSensitivity("invalid"), false, false, "invalid"},
	}

	for _, tt := range tests {
		t.Run(string(tt.val), func(t *testing.T) {
			if tt.val.IsValid() != tt.wantValid {
				t.Errorf("IsValid() = %v, want %v", tt.val.IsValid(), tt.wantValid)
			}
			if tt.val.IsSensitive() != tt.wantSens {
				t.Errorf("IsSensitive() = %v, want %v", tt.val.IsSensitive(), tt.wantSens)
			}
			if tt.val.String() != tt.wantStrMatch {
				t.Errorf("String() = %q, want %q", tt.val.String(), tt.wantStrMatch)
			}
		})
	}
}
