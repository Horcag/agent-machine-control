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

func TestReplayMutationResultFailedProvenanceIsAllowlisted(t *testing.T) {
	effect := true
	reservation := &sessions.MutationReservation{
		OperationKind: "session.close",
		Result:        sessions.MutationResult{EffectApplied: &effect},
	}
	for _, tc := range []struct {
		name     string
		category string
		message  string
		want     error
	}{
		{name: "caller cancellation", category: "caller_canceled", message: "operation canceled by caller", want: context.Canceled},
		{name: "deadline", category: "deadline_exceeded", message: "operation deadline exceeded", want: context.DeadlineExceeded},
		{name: "session not found", category: "session_not_found", message: "session not found", want: domain.ErrSessionNotFound},
		{name: "session access denied", category: "session_access_denied", message: "session access denied", want: domain.ErrSessionAccessDenied},
		{name: "session closed", category: "session_closed", message: "session is closed", want: domain.ErrSessionClosed},
		{name: "session conflict", category: "session_conflict", message: "session conflict", want: domain.ErrSessionConflict},
		{name: "session wait timeout", category: "session_wait_timeout", message: "session wait timeout", want: domain.ErrSessionWaitTimeout},
		{name: "host key mismatch", category: "host_key_mismatch", message: "guest host key verification failed", want: domain.ErrHostKeyMismatch},
		{name: "missing host key pin", category: "missing_host_key_pin", message: "guest host key pin missing", want: domain.ErrMissingHostKeyPin},
		{name: "non canonical parameter", category: "non_canonical_parameter", message: "non-canonical session parameter", want: domain.ErrNonCanonicalParameter},
		{name: "invalid control key", category: "invalid_control_key", message: "invalid session control key", want: domain.ErrInvalidControlKey},
		{name: "invalid terminal dimensions", category: "invalid_terminal_dimensions", message: "invalid terminal dimensions", want: domain.ErrInvalidTerminalDimensions},
		{name: "invalid terminal type", category: "invalid_terminal_type", message: "invalid terminal type", want: domain.ErrInvalidTerminalType},
		{name: "invalid approval record", category: "invalid_approval_record", message: "invalid approval record", want: domain.ErrInvalidApprovalRecord},
		{name: "missing deadline", category: "missing_deadline", message: "operation deadline missing", want: domain.ErrMissingDeadline},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receipt := &domain.Receipt{Outcome: domain.ExecutionOutcome{Status: domain.OutcomeFailed, ErrorCategory: tc.category, ErrorMessage: tc.message}}
			_, _, _, err := replayMutationResult(reservation, receipt)
			if !errors.Is(err, tc.want) {
				t.Fatalf("replay error = %v, want %v", err, tc.want)
			}
		})
	}

	for _, provenance := range []domain.ExecutionOutcome{
		{Status: domain.OutcomeFailed},
		{Status: domain.OutcomeFailed, ErrorCategory: "deadline_exceeded_from_backend", ErrorMessage: "operation deadline exceeded"},
		{Status: domain.OutcomeFailed, ErrorCategory: "deadline_exceeded", ErrorMessage: "raw backend deadline text"},
	} {
		receipt := &domain.Receipt{Outcome: provenance}
		_, _, _, err := replayMutationResult(reservation, receipt)
		if err == nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			t.Errorf("untrusted provenance %+v replay error = %v, want generic failure", provenance, err)
		}
	}
}

func TestClassifyRunErrorPersistsOnlyCanonicalFailureProvenance(t *testing.T) {
	for _, failure := range canonicalSessionFailures {
		status, exitCode, category, message := classifyRunError(failure.err, true)
		wantMessage, ok := domain.CanonicalFailureMessage(failure.category)
		if status != domain.OutcomeFailed || exitCode != 1 || category != failure.category || !ok || message != wantMessage {
			t.Errorf("classify %v = (%q, %d, %q, %q), want canonical failed provenance", failure.err, status, exitCode, category, message)
		}
	}

	status, _, category, message := classifyRunError(errors.New("backend-controlled failure"), true)
	if status != domain.OutcomeFailed || category != "" || message != "" {
		t.Fatalf("untrusted failure classification = (%q, %q, %q), want failed without provenance", status, category, message)
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
