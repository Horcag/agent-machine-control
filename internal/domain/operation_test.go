package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestMachineRef_ValidationRules(t *testing.T) {
	if err := domain.MachineRef("vm-win11-alpha").Validate(); err != nil {
		t.Fatalf("expected valid ref, got %v", err)
	}
	if domain.MachineRef("vm-win11-alpha").String() != "vm-win11-alpha" {
		t.Errorf("MachineRef.String() mismatch")
	}

	assertInvalidMachineRef(t, "", "empty")
	assertInvalidMachineRef(t, domain.MachineRef(strings.Repeat("x", 257)), "too long")
	assertInvalidMachineRef(t, " vm-1", "leading space")
	assertInvalidMachineRef(t, "vm-1 ", "trailing space")
	assertInvalidMachineRef(t, "vm-\xff\xfe", "invalid utf8")
	assertInvalidMachineRef(t, "vm-1\n", "newline control char")
}

func assertInvalidMachineRef(t *testing.T, ref domain.MachineRef, reason string) {
	t.Helper()
	err := ref.Validate()
	if err == nil || !errors.Is(err, domain.ErrInvalidMachineRef) {
		t.Errorf("expected ErrInvalidMachineRef for %s (%q), got %v", reason, ref, err)
	}
}

func TestOperationKind_ValidationRules(t *testing.T) {
	kind := domain.OperationKind("machine.inspect")
	if err := kind.Validate(); err != nil {
		t.Fatalf("expected valid kind, got %v", err)
	}
	if kind.String() != "machine.inspect" {
		t.Errorf("OperationKind.String() mismatch")
	}

	tests := []struct {
		val domain.OperationKind
		lbl string
	}{
		{"", "empty"},
		{domain.OperationKind(strings.Repeat("k", 129)), "too long"},
		{" machine.start", "leading whitespace"},
		{"machine.start ", "trailing whitespace"},
		{"machine.\xff\xfe", "invalid utf8"},
		{"machine.stop\t", "tab control char"},
	}
	for _, tt := range tests {
		if err := tt.val.Validate(); err == nil || !errors.Is(err, domain.ErrInvalidOperationKind) {
			t.Errorf("expected ErrInvalidOperationKind for %s, got %v", tt.lbl, err)
		}
	}
}

func TestOperation_Validate_ValidCases(t *testing.T) {
	validActor, _ := domain.NewActorContext("user:admin", "user:admin", domain.NewScopeSet("machine:read", "machine:write"), domain.NewScopeSet("machine:read", "machine:write"))
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	observeOp := domain.Operation{
		Kind:                "machine.inspect",
		Target:              "vm-alpha",
		Actor:               validActor,
		Deadline:            now.Add(5 * time.Minute),
		RequiredCapability:  "hyperv.inspect",
		RequiredScopes:      []string{"machine:read"},
		Classification:      domain.ClassObserve,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters:          map[string]any{"detail": "full"},
	}
	if err := observeOp.Validate(); err != nil {
		t.Fatalf("expected valid observe operation, got %v", err)
	}
	fp, err := observeOp.Fingerprint()
	if err != nil || fp == "" {
		t.Fatalf("expected valid fingerprint, got %s, err %v", fp, err)
	}

	mutationOp := domain.Operation{
		Kind:                "machine.start",
		Target:              "vm-alpha",
		Actor:               validActor,
		Reason:              "operator testing workflow",
		Deadline:            now.Add(5 * time.Minute),
		IdempotencyKey:      "key-12345",
		RequiredCapability:  "hyperv.lifecycle",
		RequiredScopes:      []string{"machine:write"},
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters:          map[string]any{"force": false},
	}
	if err := mutationOp.Validate(); err != nil {
		t.Fatalf("expected valid mutation operation, got %v", err)
	}
}

func TestOperation_Validate_InvalidCases(t *testing.T) {
	validActor, _ := domain.NewActorContext("user:admin", "user:admin", domain.NewScopeSet("machine:read", "machine:write"), domain.NewScopeSet("machine:read", "machine:write"))
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		op      domain.Operation
		wantErr error
	}{
		{
			name: "invalid kind",
			op: domain.Operation{
				Kind:           "",
				Target:         "vm-alpha",
				Actor:          validActor,
				Deadline:       now.Add(5 * time.Minute),
				RequiredScopes: []string{"machine:read"},
				Classification: domain.ClassObserve,
			},
			wantErr: domain.ErrInvalidOperationKind,
		},
		{
			name: "invalid target",
			op: domain.Operation{
				Kind:           "machine.inspect",
				Target:         "",
				Actor:          validActor,
				Deadline:       now.Add(5 * time.Minute),
				RequiredScopes: []string{"machine:read"},
				Classification: domain.ClassObserve,
			},
			wantErr: domain.ErrInvalidMachineRef,
		},
		{
			name: "invalid actor",
			op: domain.Operation{
				Kind:           "machine.inspect",
				Target:         "vm-alpha",
				Actor:          domain.ActorContext{},
				Deadline:       now.Add(5 * time.Minute),
				RequiredScopes: []string{"machine:read"},
				Classification: domain.ClassObserve,
			},
			wantErr: domain.ErrInvalidActorID,
		},
		{
			name: "mutation missing idempotency key",
			op: domain.Operation{
				Kind:           "machine.start",
				Target:         "vm-alpha",
				Actor:          validActor,
				Reason:         "testing",
				Deadline:       now.Add(5 * time.Minute),
				IdempotencyKey: "",
				RequiredScopes: []string{"machine:write"},
				Classification: domain.ClassReversibleMutation,
			},
			wantErr: domain.ErrMissingIdempotencyKey,
		},
		{
			name: "mutation missing reason",
			op: domain.Operation{
				Kind:           "machine.start",
				Target:         "vm-alpha",
				Actor:          validActor,
				Reason:         "",
				Deadline:       now.Add(5 * time.Minute),
				IdempotencyKey: "key-12345",
				RequiredScopes: []string{"machine:write"},
				Classification: domain.ClassReversibleMutation,
			},
			wantErr: domain.ErrMissingReason,
		},
		{
			name: "missing deadline",
			op: domain.Operation{
				Kind:           "machine.inspect",
				Target:         "vm-alpha",
				Actor:          validActor,
				RequiredScopes: []string{"machine:read"},
				Classification: domain.ClassObserve,
			},
			wantErr: domain.ErrMissingDeadline,
		},
		{
			name: "invalid classification",
			op: domain.Operation{
				Kind:           "machine.inspect",
				Target:         "vm-alpha",
				Actor:          validActor,
				Deadline:       now.Add(5 * time.Minute),
				RequiredScopes: []string{"machine:read"},
				Classification: "invalid_class",
			},
			wantErr: domain.ErrInvalidOperationClass,
		},
		{
			name: "invalid capability leading space",
			op: domain.Operation{
				Kind:               "machine.inspect",
				Target:             "vm-alpha",
				Actor:              validActor,
				Deadline:           now.Add(5 * time.Minute),
				RequiredCapability: " hyperv.inspect",
				RequiredScopes:     []string{"machine:read"},
				Classification:     domain.ClassObserve,
			},
			wantErr: domain.ErrInvalidCapability,
		},
		{
			name: "invalid scope trailing space",
			op: domain.Operation{
				Kind:           "machine.inspect",
				Target:         "vm-alpha",
				Actor:          validActor,
				Deadline:       now.Add(5 * time.Minute),
				RequiredScopes: []string{"machine:read "},
				Classification: domain.ClassObserve,
			},
			wantErr: domain.ErrInvalidScope,
		},
		{
			name: "non-canonical parameters float",
			op: domain.Operation{
				Kind:           "machine.inspect",
				Target:         "vm-alpha",
				Actor:          validActor,
				Deadline:       now.Add(5 * time.Minute),
				RequiredScopes: []string{"machine:read"},
				Classification: domain.ClassObserve,
				Parameters:     map[string]any{"ratio": 1.5},
			},
			wantErr: domain.ErrNonCanonicalParameter,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.op.Validate()
			if err == nil {
				t.Fatalf("expected error %v, got nil", tt.wantErr)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected error matching %v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestOperation_DefensiveCloningAndNestedMutation(t *testing.T) {
	validActor, _ := domain.NewActorContext("user:admin", "user:admin", domain.NewScopeSet("machine:read"), domain.NewScopeSet("machine:read"))
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	nestedMap := map[string]any{
		"level2": map[string]any{
			"key": "val",
		},
		"slice": []any{"item1", 100},
	}

	params := map[string]any{
		"nested": nestedMap,
		"tags":   []string{"a", "b"},
	}

	scopes := []string{"machine:read", "evidence:sensitive:capture"}

	op := domain.Operation{
		Kind:                "machine.inspect",
		Target:              "vm-alpha",
		Actor:               validActor,
		Deadline:            now.Add(5 * time.Minute),
		RequiredCapability:  "hyperv.inspect",
		RequiredScopes:      scopes,
		Classification:      domain.ClassObserve,
		EvidenceSensitivity: domain.EvidenceSensitivitySensitive,
		Parameters:          params,
	}

	origFp, err := op.Fingerprint()
	if err != nil {
		t.Fatalf("unexpected error computing fingerprint: %v", err)
	}

	// Clone operation
	clonedOp := op.Clone()

	// Mutate source parameters and nested structures
	nestedMap["level2"].(map[string]any)["key"] = "mutated"
	nestedMap["slice"].([]any)[0] = "mutated_item"
	params["tags"].([]string)[0] = "mutated_tag"
	scopes[0] = "mutated_scope"

	// Verify cloned operation remains unaffected
	clonedFp, err := clonedOp.Fingerprint()
	if err != nil {
		t.Fatalf("unexpected error computing cloned fingerprint: %v", err)
	}
	if origFp != clonedFp {
		t.Fatalf("defensive cloning failed: cloned fingerprint changed from %s to %s", origFp, clonedFp)
	}

	// Verify nested values in clonedOp are unchanged
	clonedNested := clonedOp.Parameters["nested"].(map[string]any)
	if clonedNested["level2"].(map[string]any)["key"] != "val" {
		t.Errorf("nested map in cloned operation was mutated")
	}
	if clonedNested["slice"].([]any)[0] != "item1" {
		t.Errorf("nested slice in cloned operation was mutated")
	}
	if clonedOp.RequiredScopes[0] != "machine:read" {
		t.Errorf("required scopes in cloned operation was mutated")
	}
}
