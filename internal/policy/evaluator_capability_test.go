package policy_test

import (
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
)

func TestEvaluate_CapabilityEnforcement(t *testing.T) {
	now := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
	adminPerms := domain.NewScopeSet("machine:read")
	actor, _ := domain.NewActorContext("user:alice", "user:alice", adminPerms, adminPerms)

	tests := []struct {
		name        string
		reqCap      string
		availCaps   domain.CapabilitySet
		wantAllowed bool
		wantReason  policy.DenialReason
		wantMessage string
	}{
		{
			name:        "present required capability allows",
			reqCap:      "hyperv.inspect",
			availCaps:   domain.NewCapabilitySet("hyperv.inspect", "hyperv.lifecycle"),
			wantAllowed: true,
			wantReason:  policy.DenialNone,
		},
		{
			name:        "absent required capability denies with DenialMissingCapability",
			reqCap:      "hyperv.inspect",
			availCaps:   domain.NewCapabilitySet("hyperv.lifecycle"),
			wantAllowed: false,
			wantReason:  policy.DenialMissingCapability,
			wantMessage: "target backend lacks required capability",
		},
		{
			name:        "empty available capabilities with non-empty required capability denies",
			reqCap:      "hyperv.inspect",
			availCaps:   domain.NewCapabilitySet(),
			wantAllowed: false,
			wantReason:  policy.DenialMissingCapability,
			wantMessage: "target backend lacks required capability",
		},
		{
			name:        "nil available capabilities with non-empty required capability denies",
			reqCap:      "hyperv.inspect",
			availCaps:   nil,
			wantAllowed: false,
			wantReason:  policy.DenialMissingCapability,
			wantMessage: "target backend lacks required capability",
		},
		{
			name:        "empty/control-plane capability with empty available capabilities allows",
			reqCap:      "",
			availCaps:   domain.NewCapabilitySet(),
			wantAllowed: true,
			wantReason:  policy.DenialNone,
		},
		{
			name:        "empty/control-plane capability with nil available capabilities allows",
			reqCap:      "",
			availCaps:   nil,
			wantAllowed: true,
			wantReason:  policy.DenialNone,
		},
		{
			name:        "invalid required capability format denies with DenialInvalidOperation",
			reqCap:      " invalid.cap with spaces ",
			availCaps:   domain.NewCapabilitySet("invalid.cap with spaces"),
			wantAllowed: false,
			wantReason:  policy.DenialInvalidOperation,
		},
		{
			name:        "invalid available capability in policy input denies with DenialInvalidOperation",
			reqCap:      "hyperv.inspect",
			availCaps:   domain.NewCapabilitySet("bad\ncap", "hyperv.inspect"),
			wantAllowed: false,
			wantReason:  policy.DenialInvalidOperation,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := policy.EvaluationInput{
				Operation: domain.Operation{
					Kind:                "machine.inspect",
					Target:              "vm-alpha",
					Actor:               actor,
					Deadline:            now.Add(5 * time.Minute),
					RequiredCapability:  tt.reqCap,
					RequiredScopes:      []string{"machine:read"},
					Classification:      domain.ClassObserve,
					EvidenceSensitivity: domain.EvidenceSensitivityStandard,
				},
				Now:                   now,
				AuditWritable:         true,
				AvailableCapabilities: tt.availCaps,
			}

			dec := policy.Evaluate(input)
			if dec.IsAllowed() != tt.wantAllowed {
				t.Fatalf("Evaluate() allowed = %v, want %v (reason: %v, message: %q)",
					dec.IsAllowed(), tt.wantAllowed, dec.DenialReason, dec.DenialMessage)
			}
			if !tt.wantAllowed {
				if dec.DenialReason != tt.wantReason {
					t.Errorf("Evaluate() denialReason = %v, want %v", dec.DenialReason, tt.wantReason)
				}
				if tt.wantMessage != "" && dec.DenialMessage != tt.wantMessage {
					t.Errorf("Evaluate() denialMessage = %q, want %q", dec.DenialMessage, tt.wantMessage)
				}
			}
		})
	}
}
