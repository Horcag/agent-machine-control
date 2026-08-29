package operations

import (
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestMapOutcome_Default(t *testing.T) {
	state, category, msg := mapOutcome(domain.ExecutionOutcome{
		Status: domain.OutcomeStatus("unknown-status"),
	})
	if state != domain.OpStatePending {
		t.Errorf("expected OpStatePending, got %s", state)
	}
	if category != "" || msg != "" {
		t.Errorf("expected empty category/msg, got %q/%q", category, msg)
	}
}

func TestIsCompatible_Mismatches(t *testing.T) {
	now := time.Now()
	rec1 := &domain.OperationRecord{
		ReceiptID:              "rcpt-1",
		Fingerprint:            "fp-1",
		IdempotencyFingerprint: "idfp-1",
		IdempotencyKey:         "key-1",
		Actor:                  "actor-1",
		Target:                 "target-1",
		Kind:                   "kind-1",
		RequestedClass:         "class-1",
		EffectiveClass:         "class-1",
		State:                  domain.OpStateCompleted,
		ErrorCategory:          "cat-1",
		ErrorMessage:           "msg-1",
		CreatedAt:              now,
		CompletedAt:            now,
		Deadline:               now,
	}

	if !isCompatible(rec1, rec1) {
		t.Errorf("expected identical records to be compatible")
	}

	// Mismatches
	tests := []struct {
		name string
		mod  func(*domain.OperationRecord)
	}{
		{"ReceiptID", func(r *domain.OperationRecord) { r.ReceiptID = "rcpt-2" }},
		{"Fingerprint", func(r *domain.OperationRecord) { r.Fingerprint = "fp-2" }},
		{"IdempotencyFingerprint", func(r *domain.OperationRecord) { r.IdempotencyFingerprint = "idfp-2" }},
		{"IdempotencyKey", func(r *domain.OperationRecord) { r.IdempotencyKey = "key-2" }},
		{"Actor", func(r *domain.OperationRecord) { r.Actor = "actor-2" }},
		{"Target", func(r *domain.OperationRecord) { r.Target = "target-2" }},
		{"Kind", func(r *domain.OperationRecord) { r.Kind = "kind-2" }},
		{"RequestedClass", func(r *domain.OperationRecord) { r.RequestedClass = "class-2" }},
		{"EffectiveClass", func(r *domain.OperationRecord) { r.EffectiveClass = "class-2" }},
		{"State", func(r *domain.OperationRecord) { r.State = domain.OpStateFailed }},
		{"ErrorCategory", func(r *domain.OperationRecord) { r.ErrorCategory = "cat-2" }},
		{"ErrorMessage", func(r *domain.OperationRecord) { r.ErrorMessage = "msg-2" }},
		{"CreatedAt", func(r *domain.OperationRecord) { r.CreatedAt = now.Add(time.Second) }},
		{"CompletedAt", func(r *domain.OperationRecord) { r.CompletedAt = now.Add(time.Second) }},
		{"Deadline", func(r *domain.OperationRecord) { r.Deadline = now.Add(time.Second) }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec2 := *rec1
			tc.mod(&rec2)
			if isCompatible(rec1, &rec2) {
				t.Errorf("expected mismatch in %s to be incompatible", tc.name)
			}
		})
	}
}
