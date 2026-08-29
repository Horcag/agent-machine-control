package policy_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
)

func TestEvaluate_SanitizedDenialsDoNotLeakSecretInputs(t *testing.T) {
	now := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
	actor, _ := domain.NewActorContext("user:alice", "user:alice", domain.NewScopeSet("machine:read"), domain.NewScopeSet("machine:read"))

	markerScope := "synthetic-marker-unauthorized-scope"
	markerSensitiveScope := "synthetic-marker-sensitive-scope"
	markerReason := "synthetic-marker-private-reason"
	markerApprovalID := "appr-synthetic-marker-invalid"

	tests := []struct {
		name       string
		input      policy.EvaluationInput
		secretText string
	}{
		{
			name: "missing required authorization scope does not leak scope name",
			input: policy.EvaluationInput{
				Operation: domain.Operation{
					Kind:                "machine.inspect",
					Target:              "vm-alpha",
					Actor:               actor,
					Deadline:            now.Add(5 * time.Minute),
					RequiredScopes:      []string{markerScope},
					Classification:      domain.ClassObserve,
					EvidenceSensitivity: domain.EvidenceSensitivityStandard,
				},
				Now:           now,
				AuditWritable: true,
			},
			secretText: markerScope,
		},
		{
			name: "missing sensitive evidence scope does not leak sensitive scope name",
			input: policy.EvaluationInput{
				Operation: domain.Operation{
					Kind:                "machine.inspect",
					Target:              "vm-alpha",
					Actor:               actor,
					Deadline:            now.Add(5 * time.Minute),
					RequiredScopes:      []string{markerSensitiveScope},
					Classification:      domain.ClassObserve,
					EvidenceSensitivity: domain.EvidenceSensitivitySensitive,
				},
				Now:                     now,
				AuditWritable:           true,
				SensitiveEvidenceScopes: domain.NewScopeSet(markerSensitiveScope),
			},
			secretText: markerSensitiveScope,
		},
		{
			name: "missing approval does not leak reason or sensitive fields",
			input: policy.EvaluationInput{
				Operation: domain.Operation{
					Kind:                "machine.delete",
					Target:              "vm-alpha",
					Actor:               actor,
					Reason:              markerReason,
					Deadline:            now.Add(5 * time.Minute),
					IdempotencyKey:      "idemp-del",
					RequiredScopes:      []string{"machine:read"},
					Classification:      domain.ClassDestructivePrivileged,
					EvidenceSensitivity: domain.EvidenceSensitivityStandard,
				},
				Now:           now,
				AuditWritable: true,
				Approval:      nil,
			},
			secretText: markerReason,
		},
		{
			name: "malformed approval error text is not echoed",
			input: policy.EvaluationInput{
				Operation: domain.Operation{
					Kind:                "machine.delete",
					Target:              "vm-alpha",
					Actor:               actor,
					Reason:              "reason",
					Deadline:            now.Add(5 * time.Minute),
					IdempotencyKey:      "idemp-del",
					RequiredScopes:      []string{"machine:read"},
					Classification:      domain.ClassDestructivePrivileged,
					EvidenceSensitivity: domain.EvidenceSensitivityStandard,
				},
				Now:           now,
				AuditWritable: true,
				Approval: &domain.Approval{
					ID: domain.ApprovalID(markerApprovalID),
				},
			},
			secretText: markerApprovalID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec := policy.Evaluate(tt.input)
			if dec.IsAllowed() {
				t.Fatalf("expected operation to be denied")
			}
			if strings.Contains(dec.DenialMessage, tt.secretText) {
				t.Fatalf("DenialMessage leaked secret text %q: %s", tt.secretText, dec.DenialMessage)
			}
		})
	}

	// Validate helper functions do not echo raw secret inputs
	t.Run("ValidateEvidenceRef does not echo invalid file path secret", func(t *testing.T) {
		markerPath := "/synthetic/marker/forbidden/path.txt"
		err := domain.ValidateEvidenceRef(markerPath)
		if err == nil {
			t.Fatalf("expected error for file path evidence ref")
		}
		if strings.Contains(err.Error(), markerPath) {
			t.Fatalf("ValidateEvidenceRef leaked secret path in error: %v", err)
		}
	})

	t.Run("ValidateEvidenceRef does not echo space containing secret", func(t *testing.T) {
		markerSpace := "synthetic marker with space"
		err := domain.ValidateEvidenceRef(markerSpace)
		if err == nil {
			t.Fatalf("expected error for space evidence ref")
		}
		if strings.Contains(err.Error(), markerSpace) {
			t.Fatalf("ValidateEvidenceRef leaked secret string in error: %v", err)
		}
	})
}
