package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestReceipt_Validate(t *testing.T) {
	started := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	completed := started.Add(2 * time.Second)
	validFp := domain.Fingerprint("sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")

	baseReceipt := domain.Receipt{
		ReceiptID:        "rcpt-001",
		OperationKind:    "machine.inspect",
		Fingerprint:      validFp,
		Actor:            "user:alice",
		Target:           "vm-alpha",
		Class:            domain.ClassObserve,
		EffectiveBackend: "hyperv-cim",
		StartedAt:        started,
		CompletedAt:      completed,
		Outcome: domain.ExecutionOutcome{
			Status:   domain.OutcomeSuccess,
			ExitCode: 0,
		},
		ObservationType: domain.ObservationObserved,
		EvidenceRefs:    []string{"sha256:1111111111111111111111111111111111111111111111111111111111111111"},
		RedactionStatus: domain.RedactionNotApplicable,
	}

	if baseReceipt.ReceiptID.String() != "rcpt-001" {
		t.Errorf("ReceiptID.String() mismatch")
	}

	tests := []struct {
		name      string
		mutate    func(r *domain.Receipt)
		wantErrIs error
	}{
		{
			name:      "valid observe receipt",
			mutate:    func(_ *domain.Receipt) {},
			wantErrIs: nil,
		},
		{
			name: "valid mutation receipt",
			mutate: func(r *domain.Receipt) {
				r.OperationKind = "machine.start"
				r.IdempotencyKey = "key-start-1"
				r.Class = domain.ClassReversibleMutation
				r.RollbackRef = "snap-before-start"
				r.RedactionStatus = domain.RedactionApplied
			},
			wantErrIs: nil,
		},
		{
			name: "empty receipt id",
			mutate: func(r *domain.Receipt) {
				r.ReceiptID = ""
			},
			wantErrIs: domain.ErrInvalidReceiptID,
		},
		{
			name: "invalid operation kind",
			mutate: func(r *domain.Receipt) {
				r.OperationKind = ""
			},
			wantErrIs: domain.ErrInvalidOperationKind,
		},
		{
			name: "invalid actor",
			mutate: func(r *domain.Receipt) {
				r.Actor = ""
			},
			wantErrIs: domain.ErrInvalidActorID,
		},
		{
			name: "invalid target",
			mutate: func(r *domain.Receipt) {
				r.Target = ""
			},
			wantErrIs: domain.ErrInvalidMachineRef,
		},
		{
			name: "invalid operation class",
			mutate: func(r *domain.Receipt) {
				r.Class = "bad_class"
			},
			wantErrIs: domain.ErrInvalidOperationClass,
		},
		{
			name: "empty effective backend",
			mutate: func(r *domain.Receipt) {
				r.EffectiveBackend = "   "
			},
			wantErrIs: domain.ErrInvalidBackendID,
		},
		{
			name: "invalid outcome status",
			mutate: func(r *domain.Receipt) {
				r.Outcome.Status = "bad_status"
			},
			wantErrIs: domain.ErrInvalidReceiptID,
		},
		{
			name: "invalid time order (completed before started)",
			mutate: func(r *domain.Receipt) {
				r.StartedAt = completed
				r.CompletedAt = started
			},
			wantErrIs: domain.ErrInvalidReceiptTimestamps,
		},
		{
			name: "observe receipt missing fingerprint",
			mutate: func(r *domain.Receipt) {
				r.Fingerprint = ""
			},
			wantErrIs: domain.ErrMissingMutationIdentity,
		},
		{
			name: "mutation receipt missing idempotency key",
			mutate: func(r *domain.Receipt) {
				r.Class = domain.ClassReversibleMutation
				r.IdempotencyKey = ""
			},
			wantErrIs: domain.ErrMissingMutationIdentity,
		},
		{
			name: "mutation receipt invalid fingerprint",
			mutate: func(r *domain.Receipt) {
				r.Class = domain.ClassReversibleMutation
				r.IdempotencyKey = "key-1"
				r.Fingerprint = "invalid-fp"
			},
			wantErrIs: domain.ErrMissingMutationIdentity,
		},
		{
			name: "successful reversible mutation missing rollback ref",
			mutate: func(r *domain.Receipt) {
				r.OperationKind = "machine.start"
				r.IdempotencyKey = "key-1"
				r.Class = domain.ClassReversibleMutation
				r.Outcome.Status = domain.OutcomeSuccess
				r.RollbackRef = ""
			},
			wantErrIs: domain.ErrMissingMutationIdentity,
		},
		{
			name: "invalid observation type",
			mutate: func(r *domain.Receipt) {
				r.ObservationType = "guessed"
			},
			wantErrIs: domain.ErrInvalidObservationType,
		},
		{
			name: "invalid redaction status",
			mutate: func(r *domain.Receipt) {
				r.RedactionStatus = "bad_status"
			},
			wantErrIs: domain.ErrInvalidRedactionStatus,
		},
		{
			name: "redaction failed status",
			mutate: func(r *domain.Receipt) {
				r.RedactionStatus = domain.RedactionFailed
			},
			wantErrIs: domain.ErrRedactionFailed,
		},
		{
			name: "evidence ref contains file path rejected",
			mutate: func(r *domain.Receipt) {
				r.EvidenceRefs = []string{"/var/log/screens/img.png"}
			},
			wantErrIs: domain.ErrInvalidEvidenceRef,
		},
		{
			name: "evidence ref contains windows path rejected",
			mutate: func(r *domain.Receipt) {
				r.EvidenceRefs = []string{"C:\\evidence\\capture.bin"}
			},
			wantErrIs: domain.ErrInvalidEvidenceRef,
		},
		{
			name: "evidence ref contains space rejected",
			mutate: func(r *domain.Receipt) {
				r.EvidenceRefs = []string{"sha256: 12345"}
			},
			wantErrIs: domain.ErrInvalidEvidenceRef,
		},
		{
			name: "valid opaque evidence ref allowed",
			mutate: func(r *domain.Receipt) {
				r.EvidenceRefs = []string{"ev-capture-001", "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}
			},
			wantErrIs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rcpt := baseReceipt
			tt.mutate(&rcpt)
			err := rcpt.Validate()
			if tt.wantErrIs == nil {
				if err != nil {
					t.Fatalf("expected nil error, got %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("expected error %v, got nil", tt.wantErrIs)
				}
				if !errors.Is(err, tt.wantErrIs) {
					t.Fatalf("expected error %v, got %v", tt.wantErrIs, err)
				}
			}
		})
	}
}
