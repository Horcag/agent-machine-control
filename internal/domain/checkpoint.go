package domain

import (
	"fmt"
	"time"
)

// CheckpointID uniquely identifies a virtual machine checkpoint / snapshot.
type CheckpointID string

// String returns the string representation of CheckpointID.
func (id CheckpointID) String() string {
	return string(id)
}

// Validate checks that the CheckpointID is a valid canonical 36-character GUID.
func (id CheckpointID) Validate() error {
	return ValidateMachineGUID(string(id))
}

// CheckpointObservation represents a point-in-time observation of a VM checkpoint.
type CheckpointObservation struct {
	ID              string
	Name            string
	VMID            string
	ParentID        string
	CheckpointType  string
	CreatedAt       time.Time
	ObservedAt      time.Time
	ObservationType ObservationType
}

// Validate checks all domain invariants for the CheckpointObservation.
func (c CheckpointObservation) Validate() error {
	if err := ValidateMachineGUID(c.ID); err != nil {
		return fmt.Errorf("%w: invalid checkpoint id: %v", ErrInvalidCheckpointObservation, err)
	}
	if err := ValidateBoundedString(c.Name, 1, 256, ErrInvalidCheckpointObservation); err != nil {
		return fmt.Errorf("%w: invalid checkpoint name: %v", ErrInvalidCheckpointObservation, err)
	}
	if err := ValidateMachineGUID(c.VMID); err != nil {
		return fmt.Errorf("%w: invalid checkpoint vm_id: %v", ErrInvalidCheckpointObservation, err)
	}
	if c.ParentID != "" {
		if err := ValidateMachineGUID(c.ParentID); err != nil {
			return fmt.Errorf("%w: invalid checkpoint parent_id: %v", ErrInvalidCheckpointObservation, err)
		}
	}
	if c.CheckpointType != "" {
		if err := ValidateBoundedString(c.CheckpointType, 1, 128, ErrInvalidCheckpointObservation); err != nil {
			return fmt.Errorf("%w: invalid checkpoint type: %v", ErrInvalidCheckpointObservation, err)
		}
	}
	if c.CreatedAt.IsZero() {
		return fmt.Errorf("%w: created_at timestamp cannot be zero", ErrInvalidCheckpointObservation)
	}
	if c.ObservedAt.IsZero() {
		return fmt.Errorf("%w: observed_at timestamp cannot be zero", ErrInvalidCheckpointObservation)
	}
	if c.ObservationType != ObservationObserved {
		return fmt.Errorf("%w: expected %s, got %s", ErrInvalidObservationType, ObservationObserved, c.ObservationType)
	}
	return nil
}

// Clone returns a deep copy of the CheckpointObservation.
func (c CheckpointObservation) Clone() CheckpointObservation {
	return c
}
