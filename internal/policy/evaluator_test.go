package policy_test

import (
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
)

func TestDecision_Helpers(t *testing.T) {
	allow := policy.Decision{Type: policy.DecisionAllow}
	if !allow.IsAllowed() || allow.IsDenied() {
		t.Errorf("DecisionAllow helper methods unexpected: allow=%v, deny=%v", allow.IsAllowed(), allow.IsDenied())
	}

	deny := policy.Decision{Type: policy.DecisionDeny, DenialReason: policy.DenialForbidden}
	if deny.IsAllowed() || !deny.IsDenied() {
		t.Errorf("DecisionDeny helper methods unexpected: allow=%v, deny=%v", deny.IsAllowed(), deny.IsDenied())
	}
}

func TestEvaluate_ObserveAndForbidden(t *testing.T) {
	now := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
	adminPerms := domain.NewScopeSet("machine:read", "machine:write", policy.DefaultSensitiveEvidenceScope)
	adminActor, _ := domain.NewActorContext("user:alice", "user:alice", adminPerms, adminPerms)

	tests := []struct {
		name        string
		input       policy.EvaluationInput
		wantAllowed bool
		wantReason  policy.DenialReason
		wantClass   domain.OperationClass
	}{
		{
			name: "forbidden operation always denies unconditionally",
			input: policy.EvaluationInput{
				Operation: domain.Operation{
					Kind:           "host.exec",
					Target:         "vm-alpha",
					Actor:          adminActor,
					Deadline:       now.Add(5 * time.Minute),
					RequiredScopes: []string{"machine:admin"},
					Classification: domain.ClassForbidden,
				},
				Now:           now,
				AuditWritable: true,
			},
			wantAllowed: false,
			wantReason:  policy.DenialForbidden,
			wantClass:   domain.ClassForbidden,
		},
		{
			name: "zero evaluation time (Now) denies immediately",
			input: policy.EvaluationInput{
				Operation: domain.Operation{
					Kind:           "machine.inspect",
					Target:         "vm-alpha",
					Actor:          adminActor,
					Deadline:       now.Add(5 * time.Minute),
					RequiredScopes: []string{"machine:read"},
					Classification: domain.ClassObserve,
				},
				Now:           time.Time{}, // Zero timestamp
				AuditWritable: true,
			},
			wantAllowed: false,
			wantReason:  policy.DenialInvalidOperation,
		},
		{
			name: "valid observe operation allows and binds fingerprint",
			input: policy.EvaluationInput{
				Operation: domain.Operation{
					Kind:           "machine.inspect",
					Target:         "vm-alpha",
					Actor:          adminActor,
					Deadline:       now.Add(5 * time.Minute),
					RequiredScopes: []string{"machine:read"},
					Classification: domain.ClassObserve,
				},
				Now:           now,
				AuditWritable: false, // observe does not require writable audit
			},
			wantAllowed: true,
			wantReason:  policy.DenialNone,
			wantClass:   domain.ClassObserve,
		},
		{
			name: "unauthenticated caller denies",
			input: policy.EvaluationInput{
				Operation: domain.Operation{
					Kind:           "machine.inspect",
					Target:         "vm-alpha",
					Actor:          domain.ActorContext{},
					Deadline:       now.Add(5 * time.Minute),
					RequiredScopes: []string{"machine:read"},
					Classification: domain.ClassObserve,
				},
				Now:           now,
				AuditWritable: true,
			},
			wantAllowed: false,
			wantReason:  policy.DenialUnauthenticated,
		},
		{
			name: "invalid actor identifier denies",
			input: policy.EvaluationInput{
				Operation: domain.Operation{
					Kind:   "machine.inspect",
					Target: "vm-alpha",
					Actor: domain.ActorContext{
						AuthenticatedCaller: "bad\nactor",
						EffectiveActor:      "bad\nactor",
					},
					Deadline:       now.Add(5 * time.Minute),
					RequiredScopes: []string{"machine:read"},
					Classification: domain.ClassObserve,
				},
				Now:           now,
				AuditWritable: true,
			},
			wantAllowed: false,
			wantReason:  policy.DenialInvalidActor,
		},
		{
			name: "delegation exceeds caller authority denies",
			input: policy.EvaluationInput{
				Operation: domain.Operation{
					Kind:   "machine.inspect",
					Target: "vm-alpha",
					Actor: domain.ActorContext{
						AuthenticatedCaller:  "user:caller",
						EffectiveActor:       "agent:worker",
						CallerPermissions:    domain.NewScopeSet("machine:read"),
						EffectivePermissions: domain.NewScopeSet("machine:read", "machine:admin"),
					},
					Deadline:       now.Add(5 * time.Minute),
					RequiredScopes: []string{"machine:read"},
					Classification: domain.ClassObserve,
				},
				Now:           now,
				AuditWritable: true,
			},
			wantAllowed: false,
			wantReason:  policy.DenialDelegationExceeded,
		},
		{
			name: "structural validation failure invalid target denies",
			input: policy.EvaluationInput{
				Operation: domain.Operation{
					Kind:           "machine.inspect",
					Target:         "",
					Actor:          adminActor,
					Deadline:       now.Add(5 * time.Minute),
					RequiredScopes: []string{"machine:read"},
					Classification: domain.ClassObserve,
				},
				Now:           now,
				AuditWritable: true,
			},
			wantAllowed: false,
			wantReason:  policy.DenialInvalidOperation,
		},
		{
			name: "missing deadline denies",
			input: policy.EvaluationInput{
				Operation: domain.Operation{
					Kind:           "machine.inspect",
					Target:         "vm-alpha",
					Actor:          adminActor,
					RequiredScopes: []string{"machine:read"},
					Classification: domain.ClassObserve,
				},
				Now:           now,
				AuditWritable: true,
			},
			wantAllowed: false,
			wantReason:  policy.DenialInvalidOperation,
		},
		{
			name: "observe missing required scope denies",
			input: policy.EvaluationInput{
				Operation: domain.Operation{
					Kind:   "machine.inspect",
					Target: "vm-alpha",
					Actor: func() domain.ActorContext {
						p := domain.NewScopeSet("other:scope")
						ctx, _ := domain.NewActorContext("user:bob", "user:bob", p, p)
						return ctx
					}(),
					Deadline:       now.Add(5 * time.Minute),
					RequiredScopes: []string{"machine:read"},
					Classification: domain.ClassObserve,
				},
				Now:           now,
				AuditWritable: true,
			},
			wantAllowed: false,
			wantReason:  policy.DenialMissingScope,
		},
		{
			name: "observe with expired deadline denies",
			input: policy.EvaluationInput{
				Operation: domain.Operation{
					Kind:           "machine.inspect",
					Target:         "vm-alpha",
					Actor:          adminActor,
					Deadline:       now.Add(-1 * time.Minute),
					RequiredScopes: []string{"machine:read"},
					Classification: domain.ClassObserve,
				},
				Now:           now,
				AuditWritable: true,
			},
			wantAllowed: false,
			wantReason:  policy.DenialDeadlinePassed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec := policy.Evaluate(tt.input)
			if dec.IsAllowed() != tt.wantAllowed {
				t.Errorf("Evaluate() allowed = %v, want %v (reason: %v)", dec.IsAllowed(), tt.wantAllowed, dec.DenialReason)
			}
			if !tt.wantAllowed && dec.DenialReason != tt.wantReason {
				t.Errorf("Evaluate() denialReason = %v, want %v", dec.DenialReason, tt.wantReason)
			}
			if tt.wantAllowed {
				if dec.EffectiveClass != tt.wantClass {
					t.Errorf("Evaluate() effectiveClass = %v, want %v", dec.EffectiveClass, tt.wantClass)
				}
				expectedFp, _ := tt.input.Operation.Fingerprint()
				if dec.OperationFingerprint != expectedFp {
					t.Errorf("Evaluate() OperationFingerprint = %v, want %v", dec.OperationFingerprint, expectedFp)
				}
			}
		})
	}
}

func TestEvaluate_SensitiveEvidenceCapture(t *testing.T) {
	now := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
	withEvidenceScope := domain.NewScopeSet("machine:read", policy.DefaultSensitiveEvidenceScope)
	withoutEvidenceScope := domain.NewScopeSet("machine:read")

	actorWithScope, _ := domain.NewActorContext("user:alice", "user:alice", withEvidenceScope, withEvidenceScope)
	actorWithoutScope, _ := domain.NewActorContext("user:bob", "user:bob", withoutEvidenceScope, withoutEvidenceScope)

	// Screenshot operation with explicit sensitive scopes: permitted as Observe WITHOUT needing destructive approval
	inputWithScope := policy.EvaluationInput{
		Operation: domain.Operation{
			Kind:                "console.screenshot",
			Target:              "vm-alpha",
			Actor:               actorWithScope,
			Deadline:            now.Add(5 * time.Minute),
			RequiredCapability:  "hyperv.framebuffer",
			RequiredScopes:      []string{"machine:read", policy.DefaultSensitiveEvidenceScope},
			Classification:      domain.ClassObserve,
			EvidenceSensitivity: domain.EvidenceSensitivitySensitive,
		},
		Now:                     now,
		AuditWritable:           true,
		SensitiveEvidenceScopes: domain.NewScopeSet(policy.DefaultSensitiveEvidenceScope),
		AvailableCapabilities:   domain.NewCapabilitySet("hyperv.framebuffer"),
	}

	dec := policy.Evaluate(inputWithScope)
	if !dec.IsAllowed() {
		t.Fatalf("expected sensitive-evidence capture with scope to be allowed, got denial: %v", dec.DenialReason)
	}
	if dec.EffectiveClass != domain.ClassObserve {
		t.Errorf("expected ClassObserve, got %v", dec.EffectiveClass)
	}
	expectedFp, _ := inputWithScope.Operation.Fingerprint()
	if dec.OperationFingerprint != expectedFp {
		t.Errorf("expected bound fingerprint %s, got %s", expectedFp, dec.OperationFingerprint)
	}

	// Screenshot operation without sensitive scope: denied
	inputWithoutScope := policy.EvaluationInput{
		Operation: domain.Operation{
			Kind:                "console.screenshot",
			Target:              "vm-alpha",
			Actor:               actorWithoutScope,
			Deadline:            now.Add(5 * time.Minute),
			RequiredCapability:  "hyperv.framebuffer",
			RequiredScopes:      []string{"machine:read", policy.DefaultSensitiveEvidenceScope},
			Classification:      domain.ClassObserve,
			EvidenceSensitivity: domain.EvidenceSensitivitySensitive,
		},
		Now:                     now,
		AuditWritable:           true,
		SensitiveEvidenceScopes: domain.NewScopeSet(policy.DefaultSensitiveEvidenceScope),
		AvailableCapabilities:   domain.NewCapabilitySet("hyperv.framebuffer"),
	}

	dec2 := policy.Evaluate(inputWithoutScope)
	if dec2.IsAllowed() {
		t.Fatalf("expected sensitive-evidence capture without scope to be denied")
	}
	if dec2.DenialReason != policy.DenialMissingSensitiveEvidenceScope {
		t.Errorf("expected DenialMissingSensitiveEvidenceScope, got %v", dec2.DenialReason)
	}

	// No keyword classification regression test: arbitrary operation kind with sensitive evidence sensitivity
	inputCustomKind := policy.EvaluationInput{
		Operation: domain.Operation{
			Kind:                "custom.read_binary",
			Target:              "vm-alpha",
			Actor:               actorWithoutScope,
			Deadline:            now.Add(5 * time.Minute),
			RequiredScopes:      []string{"machine:read"},
			Classification:      domain.ClassObserve,
			EvidenceSensitivity: domain.EvidenceSensitivitySensitive,
		},
		Now:                     now,
		AuditWritable:           true,
		SensitiveEvidenceScopes: domain.NewScopeSet(policy.DefaultSensitiveEvidenceScope),
	}
	dec3 := policy.Evaluate(inputCustomKind)
	if dec3.IsAllowed() {
		t.Fatalf("expected custom kind with sensitive evidence sensitivity to be denied without sensitive scope")
	}
	if dec3.DenialReason != policy.DenialMissingSensitiveEvidenceScope {
		t.Errorf("expected DenialMissingSensitiveEvidenceScope, got %v", dec3.DenialReason)
	}
}

func TestEvaluate_ActorDelegationAndPromptAssertions(t *testing.T) {
	now := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
	callerPerms := domain.NewScopeSet("machine:read")
	effectivePerms := domain.NewScopeSet("machine:read")

	validDelegated, _ := domain.NewActorContext("user:admin", "agent:bot", callerPerms, effectivePerms)

	// Valid delegated read operation
	op := domain.Operation{
		Kind:           "machine.inspect",
		Target:         "vm-alpha",
		Actor:          validDelegated,
		Deadline:       now.Add(5 * time.Minute),
		RequiredScopes: []string{"machine:read"},
		Classification: domain.ClassObserve,
	}

	dec := policy.Evaluate(policy.EvaluationInput{
		Operation:     op,
		Now:           now,
		AuditWritable: true,
	})

	if !dec.IsAllowed() {
		t.Fatalf("expected observe operation to be allowed, got %v", dec.DenialReason)
	}

	// Operation attempting destructive action without authentic server approval: MUST DENY
	opDestructive := domain.Operation{
		Kind:           "machine.delete",
		Target:         "vm-alpha",
		Actor:          validDelegated,
		Reason:         "attempted delete",
		Deadline:       now.Add(5 * time.Minute),
		IdempotencyKey: "idemp-001",
		RequiredScopes: []string{"machine:read"},
		Classification: domain.ClassDestructivePrivileged,
	}

	decDestructive := policy.Evaluate(policy.EvaluationInput{
		Operation:     opDestructive,
		Now:           now,
		AuditWritable: true,
		Approval:      nil, // No authentic server approval
	})

	if decDestructive.IsAllowed() {
		t.Fatalf("destructive operation without server approval must NOT be allowed")
	}
	if decDestructive.DenialReason != policy.DenialApprovalRequired {
		t.Errorf("expected DenialApprovalRequired, got %v", decDestructive.DenialReason)
	}
}
