package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/sessions"
)

func TestReplayMutationResultPreservesCancellationProvenance(t *testing.T) {
	effect := false
	reservation := &sessions.MutationReservation{
		OperationKind: "session.write",
		Result:        sessions.MutationResult{EffectApplied: &effect},
	}
	for _, tc := range []struct {
		category string
		want     error
	}{
		{category: "caller_canceled", want: context.Canceled},
		{category: "deadline_exceeded", want: context.DeadlineExceeded},
	} {
		receipt := &domain.Receipt{Outcome: domain.ExecutionOutcome{Status: domain.OutcomeAborted, ErrorCategory: tc.category}}
		_, _, _, err := replayMutationResult(reservation, receipt)
		if !errors.Is(err, tc.want) {
			t.Errorf("category %q replay error = %v, want %v", tc.category, err, tc.want)
		}
	}
}

func TestMutationLeaseFingerprintIsStableAcrossDynamicSafetyClassification(t *testing.T) {
	op := domain.Operation{
		Kind: "session.open", Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Actor:  domain.ActorContext{AuthenticatedCaller: "agent:test", EffectiveActor: "agent:test"},
		Reason: "lease identity", Deadline: time.Now().Add(time.Minute), IdempotencyKey: "lease-fingerprint-1",
		RequiredCapability: domain.CapabilitySessionOpen, RequiredScopes: []string{domain.ScopeSessionOpen},
		Classification: domain.ClassReversibleMutation, EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters: map[string]any{"cols": uint16(80), "rows": uint16(24), "term": domain.DefaultTermType},
	}
	first, err := mutationLeaseFingerprint(op)
	if err != nil {
		t.Fatal(err)
	}
	op.Classification = domain.ClassDestructivePrivileged
	second, err := mutationLeaseFingerprint(op)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("lease fingerprint changed across safety classification: %s != %s", first, second)
	}
}
