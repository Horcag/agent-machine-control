package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestComputeFingerprint_DeterministicOrder(t *testing.T) {
	// Two maps with different key insertion / iteration order
	params1 := map[string]any{
		"beta":  "world",
		"alpha": "hello",
		"nested": map[string]any{
			"z": 100,
			"a": true,
		},
		"list": []any{"first", "second", 42},
	}

	params2 := map[string]any{
		"nested": map[string]any{
			"a": true,
			"z": 100,
		},
		"alpha": "hello",
		"list":  []any{"first", "second", 42},
		"beta":  "world",
	}

	deadline := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	// Scopes in different order with duplicate
	scopes1 := []string{"machine:write", "machine:read", "machine:write"}
	scopes2 := []string{"machine:read", "machine:write"}

	fp1, err := domain.ComputeFingerprint(
		"user:alice", "user:alice", "vm-1", "machine.start",
		domain.ClassReversibleMutation, "test reason", deadline, "key-123",
		"hyperv.start", scopes1, domain.EvidenceSensitivityStandard, params1,
	)
	if err != nil {
		t.Fatalf("unexpected error computing fp1: %v", err)
	}

	fp2, err := domain.ComputeFingerprint(
		"user:alice", "user:alice", "vm-1", "machine.start",
		domain.ClassReversibleMutation, "test reason", deadline, "key-123",
		"hyperv.start", scopes2, domain.EvidenceSensitivityStandard, params2,
	)
	if err != nil {
		t.Fatalf("unexpected error computing fp2: %v", err)
	}

	if fp1 != fp2 {
		t.Fatalf("expected identical fingerprints, got fp1=%s, fp2=%s", fp1, fp2)
	}
	if fp1.String() != string(fp1) {
		t.Errorf("Fingerprint.String() mismatch")
	}
	if err := fp1.Validate(); err != nil {
		t.Fatalf("fingerprint validation failed: %v", err)
	}
}

func TestComputeFingerprint_InvalidInputs(t *testing.T) {
	validCaller := domain.ActorID("user:alice")
	validEffective := domain.ActorID("user:alice")
	validTarget := domain.MachineRef("vm-1")
	validKind := domain.OperationKind("machine.start")
	deadline := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		op   domain.Operation
	}{
		{
			name: "empty caller",
			op: domain.Operation{
				Kind:           validKind,
				Target:         validTarget,
				Actor:          domain.ActorContext{EffectiveActor: validEffective},
				Deadline:       deadline,
				Classification: domain.ClassObserve,
			},
		},
		{
			name: "empty effective actor",
			op: domain.Operation{
				Kind:           validKind,
				Target:         validTarget,
				Actor:          domain.ActorContext{AuthenticatedCaller: validCaller},
				Deadline:       deadline,
				Classification: domain.ClassObserve,
			},
		},
		{
			name: "empty target",
			op: domain.Operation{
				Kind:           validKind,
				Actor:          domain.ActorContext{AuthenticatedCaller: validCaller, EffectiveActor: validEffective},
				Deadline:       deadline,
				Classification: domain.ClassObserve,
			},
		},
		{
			name: "empty kind",
			op: domain.Operation{
				Target:         validTarget,
				Actor:          domain.ActorContext{AuthenticatedCaller: validCaller, EffectiveActor: validEffective},
				Deadline:       deadline,
				Classification: domain.ClassObserve,
			},
		},
		{
			name: "zero deadline",
			op: domain.Operation{
				Kind:           validKind,
				Target:         validTarget,
				Actor:          domain.ActorContext{AuthenticatedCaller: validCaller, EffectiveActor: validEffective},
				Classification: domain.ClassObserve,
			},
		},
		{
			name: "invalid capability leading space",
			op: domain.Operation{
				Kind:               validKind,
				Target:             validTarget,
				Actor:              domain.ActorContext{AuthenticatedCaller: validCaller, EffectiveActor: validEffective},
				Deadline:           deadline,
				RequiredCapability: " bad.cap",
				Classification:     domain.ClassObserve,
			},
		},
		{
			name: "invalid scope control char",
			op: domain.Operation{
				Kind:           validKind,
				Target:         validTarget,
				Actor:          domain.ActorContext{AuthenticatedCaller: validCaller, EffectiveActor: validEffective},
				Deadline:       deadline,
				RequiredScopes: []string{"bad\nscope"},
				Classification: domain.ClassObserve,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := domain.ComputeOperationFingerprint(tt.op); err == nil {
				t.Errorf("expected error on invalid operation for %s", tt.name)
			}
		})
	}
}

func TestComputeFingerprint_AllFieldsSensitivity(t *testing.T) {
	baseOp := domain.Operation{
		Kind:                domain.OperationKind("machine.start"),
		Target:              domain.MachineRef("vm-alpha"),
		Actor:               domain.ActorContext{AuthenticatedCaller: "user:alice", EffectiveActor: "agent:worker"},
		Reason:              "operator testing workflow",
		Deadline:            time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC),
		IdempotencyKey:      "idemp-001",
		RequiredCapability:  "hyperv.lifecycle",
		RequiredScopes:      []string{"machine:write"},
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters:          map[string]any{"timeout": 30, "force": false},
	}

	baseFp, err := domain.ComputeOperationFingerprint(baseOp)
	if err != nil {
		t.Fatalf("failed to compute base fingerprint: %v", err)
	}

	variations := []struct {
		name   string
		mutate func(op *domain.Operation)
	}{
		{"changed caller", func(op *domain.Operation) { op.Actor.AuthenticatedCaller = "user:bob" }},
		{"changed effective actor", func(op *domain.Operation) { op.Actor.EffectiveActor = "agent:other" }},
		{"changed target", func(op *domain.Operation) { op.Target = "vm-beta" }},
		{"changed kind", func(op *domain.Operation) { op.Kind = "machine.stop" }},
		{"changed classification", func(op *domain.Operation) { op.Classification = domain.ClassDestructivePrivileged }},
		{"changed reason", func(op *domain.Operation) { op.Reason = "different reason for testing" }},
		{"changed deadline", func(op *domain.Operation) { op.Deadline = baseOp.Deadline.Add(10 * time.Minute) }},
		{"changed idempotency key", func(op *domain.Operation) { op.IdempotencyKey = "idemp-002" }},
		{"changed capability", func(op *domain.Operation) { op.RequiredCapability = "hyperv.other_cap" }},
		{"changed scopes", func(op *domain.Operation) { op.RequiredScopes = []string{"machine:admin"} }},
		{"changed evidence sensitivity", func(op *domain.Operation) { op.EvidenceSensitivity = domain.EvidenceSensitivitySensitive }},
		{"changed parameter value", func(op *domain.Operation) { op.Parameters = map[string]any{"timeout": 60, "force": false} }},
		{"added parameter key", func(op *domain.Operation) {
			op.Parameters = map[string]any{"timeout": 30, "force": false, "extra": "val"}
		}},
	}

	for _, v := range variations {
		t.Run(v.name, func(t *testing.T) {
			op := baseOp
			v.mutate(&op)
			fp, err := domain.ComputeOperationFingerprint(op)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if fp == baseFp {
				t.Errorf("expected different fingerprint for variation %s, but got identical %s", v.name, fp)
			}
		})
	}
}

func TestFingerprint_Validate(t *testing.T) {
	tests := []struct {
		name    string
		fp      domain.Fingerprint
		wantErr bool
	}{
		{
			name:    "valid sha256 hex",
			fp:      domain.Fingerprint("sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"),
			wantErr: false,
		},
		{
			name:    "missing sha256 prefix",
			fp:      domain.Fingerprint("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"),
			wantErr: true,
		},
		{
			name:    "too short hex",
			fp:      domain.Fingerprint("sha256:abcd"),
			wantErr: true,
		},
		{
			name:    "uppercase hex rejected",
			fp:      domain.Fingerprint("sha256:E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855"),
			wantErr: true,
		},
		{
			name:    "non-hex chars",
			fp:      domain.Fingerprint("sha256:zzb0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.fp.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Fingerprint.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && !errors.Is(err, domain.ErrInvalidFingerprint) {
				t.Errorf("expected ErrInvalidFingerprint, got %v", err)
			}
		})
	}
}
