package domain

import (
	"fmt"
	"time"
)

// ApprovalID identifies a granted approval record.
type ApprovalID string

// String returns the string representation of ApprovalID.
func (id ApprovalID) String() string {
	return string(id)
}

// Validate checks that the ApprovalID is a valid canonical identifier.
func (id ApprovalID) Validate() error {
	return ValidateApprovalID(string(id))
}

// Approval binds an explicit authorization for a single destructive or privileged operation execution.
type Approval struct {
	ID              ApprovalID
	Actor           ActorID
	Target          MachineRef
	AuthorizedClass OperationClass
	Fingerprint     Fingerprint
	IdempotencyKey  string
	IssuedAt        time.Time
	ExpiresAt       time.Time
	Consumed        bool
	ConsumedAt      *time.Time
}

// Validate checks internal consistency and invariants of the approval record itself.
func (a Approval) Validate() error {
	if err := a.validateHeader(); err != nil {
		return err
	}
	if err := a.validateTimestamps(); err != nil {
		return err
	}
	return a.validateConsumption()
}

func (a Approval) validateHeader() error {
	if err := a.ID.Validate(); err != nil {
		return fmt.Errorf("%w: invalid approval id", ErrInvalidApprovalRecord)
	}
	if err := a.Actor.Validate(); err != nil {
		return fmt.Errorf("%w: invalid actor", ErrInvalidApprovalRecord)
	}
	if err := a.Target.Validate(); err != nil {
		return fmt.Errorf("%w: invalid target", ErrInvalidApprovalRecord)
	}
	if !a.AuthorizedClass.IsValid() || !a.AuthorizedClass.RequiresApproval() {
		return fmt.Errorf("%w: invalid authorized class", ErrInvalidApprovalRecord)
	}
	if err := a.Fingerprint.Validate(); err != nil {
		return fmt.Errorf("%w: invalid fingerprint", ErrInvalidApprovalRecord)
	}
	if err := ValidateIdempotencyKey(a.IdempotencyKey); err != nil {
		return fmt.Errorf("%w: invalid idempotency key", ErrInvalidApprovalRecord)
	}
	return nil
}

func (a Approval) validateTimestamps() error {
	if a.IssuedAt.IsZero() || a.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: zero timestamp", ErrInvalidApprovalRecord)
	}
	if !a.ExpiresAt.After(a.IssuedAt) {
		return fmt.Errorf("%w: expires_at must be strictly after issued_at", ErrInvalidApprovalRecord)
	}
	return nil
}

func (a Approval) validateConsumption() error {
	if !a.Consumed && a.ConsumedAt != nil {
		return fmt.Errorf("%w: unconsumed approval must not have consumed_at timestamp", ErrInvalidApprovalRecord)
	}
	if a.Consumed {
		if a.ConsumedAt == nil || a.ConsumedAt.IsZero() {
			return fmt.Errorf("%w: consumed approval missing consumed_at timestamp", ErrInvalidApprovalRecord)
		}
		if a.ConsumedAt.Before(a.IssuedAt) {
			return fmt.Errorf("%w: consumed_at precedes issued_at", ErrInvalidApprovalRecord)
		}
		if a.ConsumedAt.After(a.ExpiresAt) {
			return fmt.Errorf("%w: consumed_at exceeds expires_at", ErrInvalidApprovalRecord)
		}
	}
	return nil
}

// Clone returns a deep copy of the Approval record.
func (a Approval) Clone() Approval {
	cloned := a
	if a.ConsumedAt != nil {
		t := *a.ConsumedAt
		cloned.ConsumedAt = &t
	}
	return cloned
}

// Matches verifies that the approval is bound to the exact actor, target, fingerprint, and idempotency key of the operation.
func (a Approval) Matches(op Operation) error {
	if a.Actor != op.Actor.EffectiveActor {
		return ErrApprovalActorMismatch
	}
	if a.Target != op.Target {
		return ErrApprovalTargetMismatch
	}
	if a.IdempotencyKey != op.IdempotencyKey {
		return ErrApprovalKeyMismatch
	}

	fp, err := op.Fingerprint()
	if err != nil {
		return fmt.Errorf("failed to compute operation fingerprint for approval match: %w", err)
	}
	if a.Fingerprint != fp {
		return ErrApprovalFingerprintMismatch
	}
	return nil
}

// MatchesEffectiveClass checks whether the approval authorizes the given effective operation class.
func (a Approval) MatchesEffectiveClass(effectiveClass OperationClass) error {
	if a.AuthorizedClass != effectiveClass {
		return ErrApprovalClassMismatch
	}
	return nil
}

// IsActive checks if the approval is valid at the specified time and has not been consumed.
func (a Approval) IsActive(now time.Time) error {
	if now.IsZero() {
		return fmt.Errorf("%w: evaluation timestamp is zero", ErrInvalidApprovalRecord)
	}
	if a.Consumed {
		if a.ConsumedAt != nil && a.ConsumedAt.After(now) {
			return fmt.Errorf("%w: consumed_at timestamp is in the future relative to evaluation time", ErrInvalidApprovalRecord)
		}
		return ErrApprovalConsumed
	}
	if now.Before(a.IssuedAt) {
		return ErrApprovalNotYetValid
	}
	if !now.Before(a.ExpiresAt) {
		return ErrApprovalExpired
	}
	return nil
}

// ValidateForEffectiveOperation performs complete validation of the approval against an operation and its effective class at the given time.
func (a Approval) ValidateForEffectiveOperation(op Operation, effectiveClass OperationClass, now time.Time) error {
	if err := a.Validate(); err != nil {
		return err
	}
	if err := a.Matches(op); err != nil {
		return err
	}
	if err := a.MatchesEffectiveClass(effectiveClass); err != nil {
		return err
	}
	if err := a.IsActive(now); err != nil {
		return err
	}
	return nil
}

// ValidateForOperation performs complete validation of the approval against an operation at the given time.
func (a Approval) ValidateForOperation(op Operation, now time.Time) error {
	return a.ValidateForEffectiveOperation(op, op.Classification, now)
}
