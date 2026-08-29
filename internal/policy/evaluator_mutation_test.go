package policy_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
)

func TestEvaluate_MutationAuditAndIdempotency(t *testing.T) {
	now := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
	adminPerms := domain.NewScopeSet("machine:write")
	actor, _ := domain.NewActorContext("user:alice", "user:alice", adminPerms, adminPerms)

	baseMutation := domain.Operation{
		Kind:                "machine.start",
		Target:              "vm-alpha",
		Actor:               actor,
		Reason:              "starting workload",
		Deadline:            now.Add(5 * time.Minute),
		IdempotencyKey:      "idemp-start-1",
		RequiredCapability:  "hyperv.lifecycle",
		RequiredScopes:      []string{"machine:write"},
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	// Unwritable audit must deny mutation
	decAuditUnwritable := policy.Evaluate(policy.EvaluationInput{
		Operation:             baseMutation,
		Now:                   now,
		AuditWritable:         false,
		RollbackState:         policy.RollbackState{Available: true, Verified: true, CheckpointID: "snap-1"},
		AvailableCapabilities: domain.NewCapabilitySet("hyperv.lifecycle"),
	})
	if decAuditUnwritable.IsAllowed() {
		t.Fatalf("expected mutation to be denied when audit is unwritable")
	}
	if decAuditUnwritable.DenialReason != policy.DenialAuditUnwritable {
		t.Errorf("expected DenialAuditUnwritable, got %v", decAuditUnwritable.DenialReason)
	}

	// Missing idempotency key
	opMissingKey := baseMutation
	opMissingKey.IdempotencyKey = ""
	decMissingKey := policy.Evaluate(policy.EvaluationInput{
		Operation:             opMissingKey,
		Now:                   now,
		AuditWritable:         true,
		RollbackState:         policy.RollbackState{Available: true, Verified: true, CheckpointID: "snap-1"},
		AvailableCapabilities: domain.NewCapabilitySet("hyperv.lifecycle"),
	})
	if decMissingKey.IsAllowed() {
		t.Fatalf("expected mutation without idempotency key to be denied")
	}
	if decMissingKey.DenialReason != policy.DenialMissingIdempotencyKey {
		t.Errorf("expected DenialMissingIdempotencyKey, got %v", decMissingKey.DenialReason)
	}

	// Missing reason
	opMissingReason := baseMutation
	opMissingReason.Reason = ""
	decMissingReason := policy.Evaluate(policy.EvaluationInput{
		Operation:             opMissingReason,
		Now:                   now,
		AuditWritable:         true,
		RollbackState:         policy.RollbackState{Available: true, Verified: true, CheckpointID: "snap-1"},
		AvailableCapabilities: domain.NewCapabilitySet("hyperv.lifecycle"),
	})
	if decMissingReason.IsAllowed() {
		t.Fatalf("expected mutation without reason to be denied")
	}
	if decMissingReason.DenialReason != policy.DenialMissingReason {
		t.Errorf("expected DenialMissingReason, got %v", decMissingReason.DenialReason)
	}
}

func TestEvaluate_RollbackReclassification(t *testing.T) {
	now := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
	adminPerms := domain.NewScopeSet("machine:write", "machine:admin")
	actor, _ := domain.NewActorContext("user:alice", "user:alice", adminPerms, adminPerms)

	revOp := domain.Operation{
		Kind:                "machine.start",
		Target:              "vm-alpha",
		Actor:               actor,
		Reason:              "starting workload",
		Deadline:            now.Add(5 * time.Minute),
		IdempotencyKey:      "idemp-001",
		RequiredCapability:  "hyperv.lifecycle",
		RequiredScopes:      []string{"machine:write"},
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters:          map[string]any{"force": true},
	}

	fp, err := revOp.Fingerprint()
	if err != nil {
		t.Fatalf("unexpected error computing fingerprint: %v", err)
	}

	// 1. Reversible with verified rollback -> Allowed as ClassReversibleMutation with bound checkpoint reference and fingerprint
	decWithRollback := policy.Evaluate(policy.EvaluationInput{
		Operation:     revOp,
		Now:           now,
		AuditWritable: true,
		RollbackState: policy.RollbackState{
			Available:    true,
			Verified:     true,
			CheckpointID: "snap-100",
		},
		AvailableCapabilities: domain.NewCapabilitySet("hyperv.lifecycle"),
	})
	if !decWithRollback.IsAllowed() {
		t.Fatalf("expected allowed reversible mutation, got %v", decWithRollback.DenialReason)
	}
	if decWithRollback.EffectiveClass != domain.ClassReversibleMutation || decWithRollback.RollbackCheckpointID != "snap-100" {
		t.Errorf("unexpected decision properties: %v", decWithRollback)
	}
	if decWithRollback.OperationFingerprint != fp {
		t.Errorf("expected bound fingerprint %s, got %s", fp, decWithRollback.OperationFingerprint)
	}

	// 2. Reversible without verified rollback with RollbackPolicyDeny -> Denied
	decDenyPolicy := policy.Evaluate(policy.EvaluationInput{
		Operation:             revOp,
		Now:                   now,
		AuditWritable:         true,
		RollbackState:         policy.RollbackState{Available: false, Verified: false},
		RollbackPolicy:        policy.RollbackPolicyDeny,
		AvailableCapabilities: domain.NewCapabilitySet("hyperv.lifecycle"),
	})
	if decDenyPolicy.IsAllowed() || decDenyPolicy.DenialReason != policy.DenialRollbackMissing {
		t.Errorf("expected DenialRollbackMissing, got %v", decDenyPolicy.DenialReason)
	}

	// 3. Reversible without verified rollback with EscalateToDestructive without approval -> Denied with Reclassification
	decEscalateNoApproval := policy.Evaluate(policy.EvaluationInput{
		Operation:             revOp,
		Now:                   now,
		AuditWritable:         true,
		RollbackState:         policy.RollbackState{Available: false, Verified: false},
		RollbackPolicy:        policy.RollbackPolicyEscalateToDestructive,
		AvailableCapabilities: domain.NewCapabilitySet("hyperv.lifecycle"),
	})
	if decEscalateNoApproval.IsAllowed() || decEscalateNoApproval.DenialReason != policy.DenialApprovalRequired || !decEscalateNoApproval.Reclassified {
		t.Errorf("expected DenialApprovalRequired with Reclassified true, got %v", decEscalateNoApproval)
	}

	// 4. Reversible without verified rollback with matching active approval -> Allowed with Reclassification and bound fingerprint
	matchingApproval := &domain.Approval{
		ID:              "app-1",
		Actor:           "user:alice",
		Target:          "vm-alpha",
		AuthorizedClass: domain.ClassDestructivePrivileged,
		Fingerprint:     fp,
		IdempotencyKey:  "idemp-001",
		IssuedAt:        now.Add(-1 * time.Minute),
		ExpiresAt:       now.Add(5 * time.Minute),
		Consumed:        false,
	}

	decEscalateWithApproval := policy.Evaluate(policy.EvaluationInput{
		Operation:             revOp,
		Now:                   now,
		AuditWritable:         true,
		RollbackState:         policy.RollbackState{Available: false, Verified: false},
		RollbackPolicy:        policy.RollbackPolicyEscalateToDestructive,
		Approval:              matchingApproval,
		AvailableCapabilities: domain.NewCapabilitySet("hyperv.lifecycle"),
	})
	if !decEscalateWithApproval.IsAllowed() || decEscalateWithApproval.EffectiveClass != domain.ClassDestructivePrivileged || !decEscalateWithApproval.Reclassified {
		t.Errorf("expected allowed with Reclassified true, got %v", decEscalateWithApproval)
	}
	if decEscalateWithApproval.OperationFingerprint != fp {
		t.Errorf("expected bound fingerprint %s, got %s", fp, decEscalateWithApproval.OperationFingerprint)
	}
}

func TestEvaluate_RollbackFailClosedIncoherentStates(t *testing.T) {
	now := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
	adminPerms := domain.NewScopeSet("machine:write", "machine:admin")
	actor, _ := domain.NewActorContext("user:alice", "user:alice", adminPerms, adminPerms)

	revOp := domain.Operation{
		Kind:                "machine.start",
		Target:              "vm-alpha",
		Actor:               actor,
		Reason:              "starting workload",
		Deadline:            now.Add(5 * time.Minute),
		IdempotencyKey:      "idemp-001",
		RequiredCapability:  "hyperv.lifecycle",
		RequiredScopes:      []string{"machine:write"},
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters:          map[string]any{"force": true},
	}

	// 1. Incoherent RollbackState: Verified without Available -> MUST DENY
	decIncoherent1 := policy.Evaluate(policy.EvaluationInput{
		Operation:     revOp,
		Now:           now,
		AuditWritable: true,
		RollbackState: policy.RollbackState{
			Available:    false,
			Verified:     true,
			CheckpointID: "snap-100",
		},
		AvailableCapabilities: domain.NewCapabilitySet("hyperv.lifecycle"),
	})
	if decIncoherent1.IsAllowed() || decIncoherent1.DenialReason != policy.DenialRollbackMissing {
		t.Errorf("expected DenialRollbackMissing for verified without available, got %v (allowed=%v)", decIncoherent1.DenialReason, decIncoherent1.IsAllowed())
	}

	// 2. Incoherent RollbackState: CheckpointID present when unavailable -> MUST DENY
	decIncoherent2 := policy.Evaluate(policy.EvaluationInput{
		Operation:     revOp,
		Now:           now,
		AuditWritable: true,
		RollbackState: policy.RollbackState{
			Available:    false,
			Verified:     false,
			CheckpointID: "snap-100",
		},
		AvailableCapabilities: domain.NewCapabilitySet("hyperv.lifecycle"),
	})
	if decIncoherent2.IsAllowed() || decIncoherent2.DenialReason != policy.DenialRollbackMissing {
		t.Errorf("expected DenialRollbackMissing for checkpoint ID present when unavailable, got %v", decIncoherent2.DenialReason)
	}

	// 3. Incoherent RollbackState: Available and Verified but empty CheckpointID -> MUST DENY
	decIncoherent3 := policy.Evaluate(policy.EvaluationInput{
		Operation:     revOp,
		Now:           now,
		AuditWritable: true,
		RollbackState: policy.RollbackState{
			Available:    true,
			Verified:     true,
			CheckpointID: "",
		},
		AvailableCapabilities: domain.NewCapabilitySet("hyperv.lifecycle"),
	})
	if decIncoherent3.IsAllowed() || decIncoherent3.DenialReason != policy.DenialRollbackMissing {
		t.Errorf("expected DenialRollbackMissing for empty checkpoint ID on verified rollback, got %v", decIncoherent3.DenialReason)
	}

	// 4. Incoherent RollbackState: Available and Verified but invalid CheckpointID (whitespace) -> MUST DENY
	decIncoherent4 := policy.Evaluate(policy.EvaluationInput{
		Operation:     revOp,
		Now:           now,
		AuditWritable: true,
		RollbackState: policy.RollbackState{
			Available:    true,
			Verified:     true,
			CheckpointID: " snap-100 ",
		},
		AvailableCapabilities: domain.NewCapabilitySet("hyperv.lifecycle"),
	})
	if decIncoherent4.IsAllowed() || decIncoherent4.DenialReason != policy.DenialRollbackMissing {
		t.Errorf("expected DenialRollbackMissing for whitespace in checkpoint ID, got %v", decIncoherent4.DenialReason)
	}

	// 5. Unknown RollbackPolicy -> MUST DENY
	decUnknownPolicy := policy.Evaluate(policy.EvaluationInput{
		Operation:             revOp,
		Now:                   now,
		AuditWritable:         true,
		RollbackState:         policy.RollbackState{Available: false, Verified: false},
		RollbackPolicy:        policy.RollbackPolicy("unknown_custom_policy"),
		AvailableCapabilities: domain.NewCapabilitySet("hyperv.lifecycle"),
	})
	if decUnknownPolicy.IsAllowed() || decUnknownPolicy.DenialReason != policy.DenialInvalidOperation {
		t.Errorf("expected DenialInvalidOperation for unknown rollback policy, got %v", decUnknownPolicy.DenialReason)
	}
}

func TestEvaluate_ApprovalValidationErrors(t *testing.T) {
	now := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
	adminPerms := domain.NewScopeSet("machine:write", "machine:admin")
	actor, _ := domain.NewActorContext("user:alice", "user:alice", adminPerms, adminPerms)

	destOp := domain.Operation{
		Kind:                "machine.delete",
		Target:              "vm-alpha",
		Actor:               actor,
		Reason:              "deleting",
		Deadline:            now.Add(5 * time.Minute),
		IdempotencyKey:      "idemp-001",
		RequiredCapability:  "hyperv.lifecycle",
		RequiredScopes:      []string{"machine:admin"},
		Classification:      domain.ClassDestructivePrivileged,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}
	fp, err := destOp.Fingerprint()
	if err != nil {
		t.Fatalf("failed to compute fingerprint: %v", err)
	}

	consumedTime := now.Add(-10 * time.Second)

	tests := []struct {
		name       string
		approval   *domain.Approval
		checkTime  time.Time
		wantReason policy.DenialReason
	}{
		{
			name: "consumed approval",
			approval: &domain.Approval{
				ID: "app-1", Actor: "user:alice", Target: "vm-alpha", AuthorizedClass: domain.ClassDestructivePrivileged, Fingerprint: fp, IdempotencyKey: "idemp-001",
				IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute), Consumed: true, ConsumedAt: &consumedTime,
			},
			checkTime:  now,
			wantReason: policy.DenialApprovalConsumed,
		},
		{
			name:       "malformed approval",
			approval:   &domain.Approval{ID: ""},
			checkTime:  now,
			wantReason: policy.DenialApprovalMismatch,
		},
		{
			name: "mismatched target",
			approval: &domain.Approval{
				ID: "app-1", Actor: "user:alice", Target: "vm-beta", AuthorizedClass: domain.ClassDestructivePrivileged, Fingerprint: fp, IdempotencyKey: "idemp-001",
				IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute),
			},
			checkTime:  now,
			wantReason: policy.DenialApprovalMismatch,
		},
		{
			name: "expired approval",
			approval: &domain.Approval{
				ID: "app-1", Actor: "user:alice", Target: "vm-alpha", AuthorizedClass: domain.ClassDestructivePrivileged, Fingerprint: fp, IdempotencyKey: "idemp-001",
				IssuedAt: now.Add(-10 * time.Minute), ExpiresAt: now.Add(-time.Minute),
			},
			checkTime:  now,
			wantReason: policy.DenialApprovalExpired,
		},
		{
			name: "not yet valid approval",
			approval: &domain.Approval{
				ID: "app-1", Actor: "user:alice", Target: "vm-alpha", AuthorizedClass: domain.ClassDestructivePrivileged, Fingerprint: fp, IdempotencyKey: "idemp-001",
				IssuedAt: now.Add(time.Minute), ExpiresAt: now.Add(10 * time.Minute),
			},
			checkTime:  now,
			wantReason: policy.DenialApprovalNotYetValid,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec := policy.Evaluate(policy.EvaluationInput{
				Operation:             destOp,
				Now:                   tt.checkTime,
				AuditWritable:         true,
				Approval:              tt.approval,
				AvailableCapabilities: domain.NewCapabilitySet("hyperv.lifecycle"),
			})
			if dec.IsAllowed() || dec.DenialReason != tt.wantReason {
				t.Errorf("expected denial with reason %v, got %v (allowed=%v)", tt.wantReason, dec.DenialReason, dec.IsAllowed())
			}
		})
	}
}

func TestEvaluate_NoErrorLeakingSecretsOrParameters(t *testing.T) {
	now := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
	adminPerms := domain.NewScopeSet("machine:write")
	actor, _ := domain.NewActorContext("user:alice", "user:alice", adminPerms, adminPerms)

	secretParam := "SuperSecretPassword123!"
	op := domain.Operation{
		Kind:                "machine.start",
		Target:              "vm-alpha",
		Actor:               actor,
		Reason:              "test secret",
		Deadline:            now.Add(5 * time.Minute),
		IdempotencyKey:      "key-1",
		RequiredScopes:      []string{"missing:scope"},
		Classification:      domain.ClassObserve,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters:          map[string]any{"password": secretParam},
	}

	dec := policy.Evaluate(policy.EvaluationInput{
		Operation:     op,
		Now:           now,
		AuditWritable: true,
	})

	if dec.IsAllowed() {
		t.Fatalf("expected denial")
	}

	// Verify that the denial message does not leak the parameter value or sensitive keywords
	if strings.Contains(dec.DenialMessage, secretParam) {
		t.Fatalf("DenialMessage leaked secret parameter: %s", dec.DenialMessage)
	}
}
