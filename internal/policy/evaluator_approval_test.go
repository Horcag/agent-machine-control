package policy_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
)

func TestEvaluate_EffectiveApprovalClassDirectDestructive(t *testing.T) {
	now := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
	adminPerms := domain.NewScopeSet("machine:admin")
	actor, _ := domain.NewActorContext("user:admin", "user:admin", adminPerms, adminPerms)

	destOp := domain.Operation{
		Kind:                "machine.delete",
		Target:              "vm-alpha",
		Actor:               actor,
		Reason:              "decommissioning vm",
		Deadline:            now.Add(10 * time.Minute),
		IdempotencyKey:      "idemp-del-01",
		RequiredCapability:  "hyperv.lifecycle",
		RequiredScopes:      []string{"machine:admin"},
		Classification:      domain.ClassDestructivePrivileged,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	fp, err := destOp.Fingerprint()
	if err != nil {
		t.Fatalf("unexpected error computing fingerprint: %v", err)
	}

	// 1. Direct destructive operation with matching ClassDestructivePrivileged approval -> Allowed
	validApproval := &domain.Approval{
		ID:              "app-dest-01",
		Actor:           "user:admin",
		Target:          "vm-alpha",
		AuthorizedClass: domain.ClassDestructivePrivileged,
		Fingerprint:     fp,
		IdempotencyKey:  "idemp-del-01",
		IssuedAt:        now.Add(-1 * time.Minute),
		ExpiresAt:       now.Add(5 * time.Minute),
		Consumed:        false,
	}

	decAllowed := policy.Evaluate(policy.EvaluationInput{
		Operation:             destOp,
		Now:                   now,
		AuditWritable:         true,
		Approval:              validApproval,
		AvailableCapabilities: domain.NewCapabilitySet("hyperv.lifecycle"),
	})
	if !decAllowed.IsAllowed() {
		t.Fatalf("expected direct destructive operation with valid approval to be allowed, got: %v", decAllowed.DenialReason)
	}
	if decAllowed.EffectiveClass != domain.ClassDestructivePrivileged {
		t.Errorf("expected EffectiveClass ClassDestructivePrivileged, got %v", decAllowed.EffectiveClass)
	}
	if decAllowed.Reclassified {
		t.Errorf("direct destructive operation should not be marked as reclassified")
	}
	if decAllowed.OperationFingerprint != fp {
		t.Errorf("expected bound fingerprint %s, got %s", fp, decAllowed.OperationFingerprint)
	}

	// 2. Direct destructive operation with mismatched AuthorizedClass -> Denied
	mismatchedClassApproval := &domain.Approval{
		ID:              "app-dest-02",
		Actor:           "user:admin",
		Target:          "vm-alpha",
		AuthorizedClass: domain.ClassObserve, // Mismatched / non-approval-requiring class
		Fingerprint:     fp,
		IdempotencyKey:  "idemp-del-01",
		IssuedAt:        now.Add(-1 * time.Minute),
		ExpiresAt:       now.Add(5 * time.Minute),
	}

	decMismatched := policy.Evaluate(policy.EvaluationInput{
		Operation:             destOp,
		Now:                   now,
		AuditWritable:         true,
		Approval:              mismatchedClassApproval,
		AvailableCapabilities: domain.NewCapabilitySet("hyperv.lifecycle"),
	})
	if decMismatched.IsAllowed() {
		t.Fatalf("expected direct destructive operation with mismatched approval class to be denied")
	}
	if decMismatched.DenialReason != policy.DenialApprovalMismatch {
		t.Errorf("expected DenialApprovalMismatch, got %v", decMismatched.DenialReason)
	}
	// Verify denial message is static and sanitized
	if strings.Contains(decMismatched.DenialMessage, "idemp-del-01") || strings.Contains(decMismatched.DenialMessage, "app-dest-02") {
		t.Errorf("denial message leaked raw inputs: %s", decMismatched.DenialMessage)
	}
}

func TestEvaluate_EffectiveApprovalClassReclassifiedOperation(t *testing.T) {
	now := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
	adminPerms := domain.NewScopeSet("machine:write", "machine:admin")
	actor, _ := domain.NewActorContext("user:admin", "user:admin", adminPerms, adminPerms)

	revOp := domain.Operation{
		Kind:                "machine.start",
		Target:              "vm-alpha",
		Actor:               actor,
		Reason:              "starting uncheckpointed workload",
		Deadline:            now.Add(10 * time.Minute),
		IdempotencyKey:      "idemp-start-01",
		RequiredCapability:  "hyperv.lifecycle",
		RequiredScopes:      []string{"machine:write"},
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	// Fingerprint MUST bind the original requested classification (ClassReversibleMutation)
	requestedFp, err := revOp.Fingerprint()
	if err != nil {
		t.Fatalf("unexpected error computing fingerprint: %v", err)
	}

	// 1. Reclassified operation with approval explicitly authorizing effective destructive class -> Allowed
	validDestApproval := &domain.Approval{
		ID:              "app-reclass-01",
		Actor:           "user:admin",
		Target:          "vm-alpha",
		AuthorizedClass: domain.ClassDestructivePrivileged, // Authorizes the effective destructive class
		Fingerprint:     requestedFp,                       // Binds the requested operation fingerprint
		IdempotencyKey:  "idemp-start-01",
		IssuedAt:        now.Add(-1 * time.Minute),
		ExpiresAt:       now.Add(5 * time.Minute),
		Consumed:        false,
	}

	decAllowed := policy.Evaluate(policy.EvaluationInput{
		Operation:             revOp,
		Now:                   now,
		AuditWritable:         true,
		RollbackState:         policy.RollbackState{Available: false, Verified: false},
		RollbackPolicy:        policy.RollbackPolicyEscalateToDestructive,
		Approval:              validDestApproval,
		AvailableCapabilities: domain.NewCapabilitySet("hyperv.lifecycle"),
	})
	if !decAllowed.IsAllowed() {
		t.Fatalf("expected reclassified operation with destructive approval to be allowed, got: %v", decAllowed.DenialReason)
	}
	if decAllowed.EffectiveClass != domain.ClassDestructivePrivileged {
		t.Errorf("expected EffectiveClass ClassDestructivePrivileged, got %v", decAllowed.EffectiveClass)
	}
	if !decAllowed.Reclassified {
		t.Errorf("expected Reclassified true")
	}
	if decAllowed.OperationFingerprint != requestedFp {
		t.Errorf("expected bound fingerprint %s to remain the requested operation fingerprint, got %s", requestedFp, decAllowed.OperationFingerprint)
	}

	// 2. Reclassified operation where approval fingerprint was wrongly computed for ClassDestructivePrivileged -> Denied (fingerprint mismatch)
	wrongClassOp := revOp
	wrongClassOp.Classification = domain.ClassDestructivePrivileged
	wrongFp, _ := wrongClassOp.Fingerprint()

	wrongFpApproval := &domain.Approval{
		ID:              "app-reclass-02",
		Actor:           "user:admin",
		Target:          "vm-alpha",
		AuthorizedClass: domain.ClassDestructivePrivileged,
		Fingerprint:     wrongFp, // Mismatched: not the requested operation's fingerprint
		IdempotencyKey:  "idemp-start-01",
		IssuedAt:        now.Add(-1 * time.Minute),
		ExpiresAt:       now.Add(5 * time.Minute),
	}

	decWrongFp := policy.Evaluate(policy.EvaluationInput{
		Operation:             revOp,
		Now:                   now,
		AuditWritable:         true,
		RollbackState:         policy.RollbackState{Available: false, Verified: false},
		RollbackPolicy:        policy.RollbackPolicyEscalateToDestructive,
		Approval:              wrongFpApproval,
		AvailableCapabilities: domain.NewCapabilitySet("hyperv.lifecycle"),
	})
	if decWrongFp.IsAllowed() {
		t.Fatalf("expected denial when approval fingerprint does not match requested operation fingerprint")
	}
	if decWrongFp.DenialReason != policy.DenialApprovalMismatch {
		t.Errorf("expected DenialApprovalMismatch, got %v", decWrongFp.DenialReason)
	}

	// 3. Reclassified operation without approval -> Denied with DenialApprovalRequired and Reclassified true
	decNoApproval := policy.Evaluate(policy.EvaluationInput{
		Operation:             revOp,
		Now:                   now,
		AuditWritable:         true,
		RollbackState:         policy.RollbackState{Available: false, Verified: false},
		RollbackPolicy:        policy.RollbackPolicyEscalateToDestructive,
		Approval:              nil,
		AvailableCapabilities: domain.NewCapabilitySet("hyperv.lifecycle"),
	})
	if decNoApproval.IsAllowed() {
		t.Fatalf("expected denial without approval")
	}
	if decNoApproval.DenialReason != policy.DenialApprovalRequired || !decNoApproval.Reclassified || decNoApproval.EffectiveClass != domain.ClassDestructivePrivileged {
		t.Errorf("expected DenialApprovalRequired with Reclassified true and EffectiveClass destructive, got: %v", decNoApproval)
	}
}
