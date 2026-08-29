package policy_test

import (
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
)

func TestEvaluate_DefensiveSnapshotMutationImmutability(t *testing.T) {
	now := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
	callerPerms := domain.NewScopeSet("machine:read", "machine:write")
	effectivePerms := domain.NewScopeSet("machine:read", "machine:write")
	actor, err := domain.NewActorContext("user:alice", "user:alice", callerPerms, effectivePerms)
	if err != nil {
		t.Fatalf("unexpected error creating actor context: %v", err)
	}

	nestedMap := map[string]any{
		"config": map[string]any{
			"boot_order": []any{"disk", "net"},
			"memory_mb":  4096,
		},
		"tags": []string{"production", "web"},
	}

	requiredScopes := []string{"machine:read", "machine:write"}

	op := domain.Operation{
		Kind:                "machine.start",
		Target:              "vm-alpha",
		Actor:               actor,
		Reason:              "initiating workload",
		Deadline:            now.Add(10 * time.Minute),
		IdempotencyKey:      "idemp-start-99",
		RequiredCapability:  "hyperv.lifecycle",
		RequiredScopes:      requiredScopes,
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters:          nestedMap,
	}

	expectedFp, err := op.Fingerprint()
	if err != nil {
		t.Fatalf("unexpected error computing fingerprint: %v", err)
	}

	input := policy.EvaluationInput{
		Operation:     op,
		Now:           now,
		AuditWritable: true,
		RollbackState: policy.RollbackState{
			Available:    true,
			Verified:     true,
			CheckpointID: "snap-valid-100",
		},
		AvailableCapabilities: domain.NewCapabilitySet("hyperv.lifecycle"),
	}

	// Evaluate the input
	dec := policy.Evaluate(input)
	if !dec.IsAllowed() {
		t.Fatalf("expected decision to be allowed, got denial: %v", dec.DenialReason)
	}
	if dec.OperationFingerprint != expectedFp {
		t.Fatalf("expected bound fingerprint %s, got %s", expectedFp, dec.OperationFingerprint)
	}

	// Now aggressively mutate the original input structures
	nestedMap["config"].(map[string]any)["memory_mb"] = 8192
	nestedMap["config"].(map[string]any)["boot_order"].([]any)[0] = "mutated_disk"
	nestedMap["tags"].([]string)[0] = "mutated_tag"
	requiredScopes[0] = "mutated:scope"
	callerPerms["machine:admin"] = struct{}{}
	effectivePerms["machine:admin"] = struct{}{}

	// Verify that the previously evaluated decision's bound fingerprint is still the original expected fingerprint
	if dec.OperationFingerprint != expectedFp {
		t.Errorf("decision fingerprint changed after mutating input: got %s, want %s",
			dec.OperationFingerprint, expectedFp)
	}

	// Verify that if we evaluate again with the mutated input, the new fingerprint is different from the original
	decMutated := policy.Evaluate(input)
	// Mutated input has mutated:scope which actor lacks, so it should be denied
	if decMutated.IsAllowed() {
		t.Errorf("mutated input with missing scope should be denied")
	}
}

func TestEvaluate_ApprovalSnapshotImmutability(t *testing.T) {
	now := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
	adminPerms := domain.NewScopeSet("machine:admin")
	actor, _ := domain.NewActorContext("user:alice", "user:alice", adminPerms, adminPerms)

	destOp := domain.Operation{
		Kind:                "machine.delete",
		Target:              "vm-alpha",
		Actor:               actor,
		Reason:              "deleting vm",
		Deadline:            now.Add(10 * time.Minute),
		IdempotencyKey:      "idemp-del-1",
		RequiredCapability:  "hyperv.lifecycle",
		RequiredScopes:      []string{"machine:admin"},
		Classification:      domain.ClassDestructivePrivileged,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	fp, err := destOp.Fingerprint()
	if err != nil {
		t.Fatalf("unexpected error computing fingerprint: %v", err)
	}

	approval := &domain.Approval{
		ID:              "app-1",
		Actor:           "user:alice",
		Target:          "vm-alpha",
		AuthorizedClass: domain.ClassDestructivePrivileged,
		Fingerprint:     fp,
		IdempotencyKey:  "idemp-del-1",
		IssuedAt:        now.Add(-1 * time.Minute),
		ExpiresAt:       now.Add(10 * time.Minute),
		Consumed:        false,
	}

	input := policy.EvaluationInput{
		Operation:             destOp,
		Now:                   now,
		AuditWritable:         true,
		Approval:              approval,
		AvailableCapabilities: domain.NewCapabilitySet("hyperv.lifecycle"),
	}

	dec := policy.Evaluate(input)
	if !dec.IsAllowed() {
		t.Fatalf("expected allowed, got %v", dec.DenialReason)
	}
	if dec.OperationFingerprint != fp {
		t.Errorf("expected bound fingerprint %s, got %s", fp, dec.OperationFingerprint)
	}

	// Mutate original approval record
	approval.Consumed = true
	consumedTime := now.Add(-30 * time.Second)
	approval.ConsumedAt = &consumedTime
	approval.Target = "vm-mutated"

	// Decision remains intact
	if dec.OperationFingerprint != fp {
		t.Errorf("decision fingerprint altered by mutating approval")
	}
}
