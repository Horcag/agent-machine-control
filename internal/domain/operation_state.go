package domain

import (
	"fmt"
	"time"
)

// OperationState represents the lifecycle state of a durable operation.
type OperationState string

const (
	// OpStatePending indicates the operation is queued and awaiting admission.
	OpStatePending OperationState = "pending"

	// OpStateAdmitted indicates the operation has passed admission checks.
	OpStateAdmitted OperationState = "admitted"

	// OpStateRunning indicates the operation is actively executing on the backend.
	OpStateRunning OperationState = "running"

	// OpStateCompleted indicates the operation completed successfully.
	OpStateCompleted OperationState = "completed"

	// OpStateFailed indicates the operation failed with an error.
	OpStateFailed OperationState = "failed"

	// OpStateCancelled indicates the operation was cancelled or aborted.
	OpStateCancelled OperationState = "cancelled"
)

// IsTerminal returns true if the state represents a final terminal state.
func (s OperationState) IsTerminal() bool {
	return s == OpStateCompleted || s == OpStateFailed || s == OpStateCancelled
}

// IsValid checks if the state is one of the recognized OperationState constants.
func (s OperationState) IsValid() bool {
	switch s {
	case OpStatePending, OpStateAdmitted, OpStateRunning, OpStateCompleted, OpStateFailed, OpStateCancelled:
		return true
	default:
		return false
	}
}

var validTransitions = map[OperationState]map[OperationState]bool{
	OpStatePending:  {OpStateAdmitted: true, OpStateCancelled: true, OpStateFailed: true},
	OpStateAdmitted: {OpStateRunning: true, OpStateCancelled: true, OpStateFailed: true},
	OpStateRunning:  {OpStateCompleted: true, OpStateFailed: true, OpStateCancelled: true},
}

// ValidateStateTransition checks if transitioning from one state to another is legal.
func ValidateStateTransition(from, to OperationState) error {
	if !from.IsValid() {
		return fmt.Errorf("%w: unknown source state %q", ErrInvalidOperationState, from)
	}
	if !to.IsValid() {
		return fmt.Errorf("%w: unknown target state %q", ErrInvalidOperationState, to)
	}
	if from == to || validTransitions[from][to] {
		return nil
	}
	if from.IsTerminal() {
		return fmt.Errorf("%w: cannot transition from terminal state %q to %q", ErrIllegalStateTransition, from, to)
	}
	return fmt.Errorf("%w: cannot transition from %q to %q", ErrIllegalStateTransition, from, to)
}

// OperationRecord represents the durable, crash-safe state of an operation on disk.
type OperationRecord struct {
	SchemaVersion  string         `json:"schema_version"`
	ID             string         `json:"id"`
	Actor          ActorID        `json:"actor"`
	Target         MachineRef     `json:"target"`
	Kind           OperationKind  `json:"kind"`
	RequestedClass OperationClass `json:"requested_class"`
	EffectiveClass OperationClass `json:"effective_class"`
	Fingerprint    Fingerprint    `json:"fingerprint"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	Deadline       time.Time      `json:"deadline"`
	State          OperationState `json:"state"`
	CreatedAt      time.Time      `json:"created_at"`
	AdmittedAt     time.Time      `json:"admitted_at"`
	RunningAt      time.Time      `json:"running_at"`
	CompletedAt    time.Time      `json:"completed_at"`
	LastEventSeq   uint64         `json:"last_event_seq,omitempty"`
	ReceiptID      ReceiptID      `json:"receipt_id,omitempty"`
	ErrorCategory  string         `json:"error_category,omitempty"`
	ErrorMessage   string         `json:"error_message,omitempty"`
	Parameters     map[string]any `json:"parameters,omitempty"`
}

// Validate verifies that the OperationRecord invariants hold.
func (r OperationRecord) Validate() error {
	if r.SchemaVersion != "1" {
		return fmt.Errorf("invalid schema version %q", r.SchemaVersion)
	}
	if r.ID == "" {
		return ErrInvalidOperationID
	}
	if err := r.Actor.Validate(); err != nil {
		return err
	}
	if err := r.Target.Validate(); err != nil {
		return err
	}
	if err := r.Kind.Validate(); err != nil {
		return err
	}
	if !r.State.IsValid() {
		return ErrInvalidOperationState
	}
	if r.CreatedAt.IsZero() {
		return ErrInvalidObservationTimestamp
	}
	return nil
}

// Clone returns a deep copy of the OperationRecord.
func (r OperationRecord) Clone() OperationRecord {
	cp := r
	cp.Parameters = DeepCloneMap(r.Parameters)
	return cp
}
