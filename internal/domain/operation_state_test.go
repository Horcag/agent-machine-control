package domain_test

import (
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestOperationState_Transitions(t *testing.T) {
	tests := []struct {
		from  domain.OperationState
		to    domain.OperationState
		valid bool
	}{
		{domain.OpStatePending, domain.OpStateAdmitted, true},
		{domain.OpStatePending, domain.OpStateCancelled, true},
		{domain.OpStatePending, domain.OpStateFailed, true},
		{domain.OpStatePending, domain.OpStateRunning, false},
		{domain.OpStatePending, domain.OpStateCompleted, false},

		{domain.OpStateAdmitted, domain.OpStateRunning, true},
		{domain.OpStateAdmitted, domain.OpStateCancelled, true},
		{domain.OpStateAdmitted, domain.OpStateFailed, true},
		{domain.OpStateAdmitted, domain.OpStatePending, false},
		{domain.OpStateAdmitted, domain.OpStateCompleted, false},

		{domain.OpStateRunning, domain.OpStateCompleted, true},
		{domain.OpStateRunning, domain.OpStateFailed, true},
		{domain.OpStateRunning, domain.OpStateCancelled, true},
		{domain.OpStateRunning, domain.OpStatePending, false},
		{domain.OpStateRunning, domain.OpStateAdmitted, false},

		{domain.OpStateCompleted, domain.OpStateRunning, false},
		{domain.OpStateFailed, domain.OpStateRunning, false},
		{domain.OpStateCancelled, domain.OpStateRunning, false},
		{"invalid_state", domain.OpStatePending, false},
		{domain.OpStatePending, "invalid_state", false},
	}

	for _, tt := range tests {
		err := domain.ValidateStateTransition(tt.from, tt.to)
		if tt.valid && err != nil {
			t.Errorf("expected transition %s -> %s to be valid, got error: %v", tt.from, tt.to, err)
		} else if !tt.valid && err == nil {
			t.Errorf("expected transition %s -> %s to be invalid, but got nil error", tt.from, tt.to)
		}
	}
}

func TestOperationRecord_Validate(t *testing.T) {
	rec := domain.OperationRecord{
		SchemaVersion: "1",
		ID:            "op-12345",
		Actor:         domain.ActorID("test-actor"),
		Target:        domain.MachineRef("c4a523d4-6b99-4d62-a5e2-4752c0f20001"),
		Kind:          domain.OperationKind("machine.start"),
		State:         domain.OpStatePending,
		CreatedAt:     time.Now().UTC(),
	}

	if err := rec.Validate(); err != nil {
		t.Fatalf("expected valid record, got: %v", err)
	}
	rec.ApprovalID = "app-operation-0123456789abcdef0123456789abcdef"
	if err := rec.Validate(); err != nil {
		t.Fatalf("expected valid approval reference, got: %v", err)
	}

	invalid := rec
	invalid.SchemaVersion = "2"
	if err := invalid.Validate(); err == nil {
		t.Errorf("expected error for schema_version 2")
	}

	invalid = rec
	invalid.ID = ""
	if err := invalid.Validate(); err == nil {
		t.Errorf("expected error for empty ID")
	}

	invalid = rec
	invalid.ApprovalID = "../bad"
	if err := invalid.Validate(); err == nil {
		t.Errorf("expected error for invalid approval ID")
	}
}

func TestEvent_Validate(t *testing.T) {
	ev := domain.Event{
		Sequence:    1,
		Timestamp:   time.Now().UTC(),
		OperationID: "op-12345",
		EventType:   "state_change",
		State:       domain.OpStatePending,
	}

	if err := ev.Validate(); err != nil {
		t.Fatalf("expected valid event, got: %v", err)
	}

	invalid := ev
	invalid.Sequence = 0
	if err := invalid.Validate(); err == nil {
		t.Errorf("expected error for sequence 0")
	}

	invalid = ev
	invalid.OperationID = ""
	if err := invalid.Validate(); err == nil {
		t.Errorf("expected error for empty operation ID")
	}
}
