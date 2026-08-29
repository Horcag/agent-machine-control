package domain

import (
	"fmt"
	"time"
)

// Event represents a structured lifecycle or progress event emitted by an operation.
type Event struct {
	Sequence    uint64         `json:"sequence"`
	Timestamp   time.Time      `json:"timestamp"`
	OperationID string         `json:"operation_id"`
	Target      MachineRef     `json:"target,omitempty"`
	EventType   string         `json:"event_type"`
	State       OperationState `json:"state"`
	Progress    float64        `json:"progress,omitempty"`
	Message     string         `json:"message,omitempty"`
	Category    string         `json:"category,omitempty"`
	ReceiptID   ReceiptID      `json:"receipt_id,omitempty"`
}

// Validate verifies that the Event fields satisfy domain rules.
func (e Event) Validate() error {
	if e.Sequence == 0 {
		return fmt.Errorf("%w: sequence must be greater than 0", ErrInvalidEventSequence)
	}
	if e.Timestamp.IsZero() {
		return ErrInvalidObservationTimestamp
	}
	if e.OperationID == "" {
		return ErrInvalidOperationID
	}
	if !e.State.IsValid() {
		return ErrInvalidOperationState
	}
	return nil
}
