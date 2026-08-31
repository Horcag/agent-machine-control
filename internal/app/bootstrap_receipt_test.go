package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestBootstrapReceiptOutcomePreservesTerminationClass(t *testing.T) {
	t.Parallel()

	deadlineCtx, cancelDeadline := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelDeadline()
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name         string
		ctx          context.Context
		effectErr    error
		wantStatus   domain.OutcomeStatus
		wantCategory string
	}{
		{name: "success", ctx: context.Background(), wantStatus: domain.OutcomeSuccess},
		{name: "deadline context", ctx: deadlineCtx, effectErr: errors.New("synthetic failure"), wantStatus: domain.OutcomeAborted, wantCategory: "deadline_exceeded"},
		{name: "deadline effect", ctx: context.Background(), effectErr: context.DeadlineExceeded, wantStatus: domain.OutcomeAborted, wantCategory: "deadline_exceeded"},
		{name: "canceled context", ctx: canceledCtx, effectErr: errors.New("synthetic failure"), wantStatus: domain.OutcomeAborted, wantCategory: "caller_canceled"},
		{name: "canceled effect", ctx: context.Background(), effectErr: context.Canceled, wantStatus: domain.OutcomeAborted, wantCategory: "caller_canceled"},
		{name: "effect failure", ctx: context.Background(), effectErr: errors.New("synthetic failure"), wantStatus: domain.OutcomeFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			outcome := bootstrapReceiptOutcome(tc.ctx, tc.effectErr)
			if outcome.Status != tc.wantStatus || outcome.ErrorCategory != tc.wantCategory {
				t.Fatalf("outcome = %#v, want status=%q category=%q", outcome, tc.wantStatus, tc.wantCategory)
			}
		})
	}
}

func TestBootstrapObservationFailureRecordsInferredTerminalEvidence(t *testing.T) {
	t.Parallel()

	observationErr := errors.New("synthetic observation failure")
	adapter := newFakeBootstrapAdapter()
	adapter.inspectErr = observationErr
	service := newTestBootstrapService(t, adapter, &fakeBootstrapDaemon{})
	result, err := service.Ensure(context.Background(), BootstrapMutationRequest{
		StateDir: t.TempDir(), Reason: "record uncertain effect", IdempotencyKey: "inferred-evidence",
		Deadline: time.Now().Add(time.Minute),
	})
	if !errors.Is(err, observationErr) || result.SchemaVersion != 0 || result.ReceiptID == "" {
		t.Fatalf("Ensure() = %#v, %v; want failed result with durable receipt", result, err)
	}
	rcpt, err := service.receiptStore.Get(result.ReceiptID)
	if err != nil {
		t.Fatalf("receipt Get() error = %v", err)
	}
	if rcpt.Outcome.Status != domain.OutcomeFailed || rcpt.ObservationType != domain.ObservationInferred {
		t.Fatalf("receipt outcome/observation = %q/%q", rcpt.Outcome.Status, rcpt.ObservationType)
	}
	if len(rcpt.EvidenceRefs) != 1 || rcpt.EvidenceRefs[0] != "bootstrap-state-unverified" {
		t.Fatalf("receipt evidence = %v, want explicit unverified state", rcpt.EvidenceRefs)
	}
}

func TestBootstrapUnverifiedPostEffectEvidenceIsInferred(t *testing.T) {
	t.Parallel()

	result := BootstrapResult{}
	if got := bootstrapReceiptObservation(result); got != domain.ObservationInferred {
		t.Fatalf("observation type = %q, want inferred", got)
	}
	evidence := bootstrapStateEvidence(result)
	if len(evidence) != 1 || evidence[0] != "bootstrap-state-unverified" {
		t.Fatalf("evidence = %v, want explicit unverified state", evidence)
	}
}
