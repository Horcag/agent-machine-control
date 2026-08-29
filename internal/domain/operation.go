package domain

import (
	"fmt"
	"slices"
	"time"
)

const (
	// MaxMachineRefLength is the maximum length of a machine reference string.
	MaxMachineRefLength = 256
	// MinMachineRefLength is the minimum length of a machine reference string.
	MinMachineRefLength = 1

	// MaxOperationKindLength is the maximum length of an operation kind string.
	MaxOperationKindLength = 128
	// MinOperationKindLength is the minimum length of an operation kind string.
	MinOperationKindLength = 1
)

// MachineRef uniquely references a target virtual machine.
type MachineRef string

// String returns the string representation of MachineRef.
func (m MachineRef) String() string {
	return string(m)
}

// Validate checks that the MachineRef is non-empty, bounded, and contains valid characters.
func (m MachineRef) Validate() error {
	return ValidateBoundedString(string(m), MinMachineRefLength, MaxMachineRefLength, ErrInvalidMachineRef)
}

// OperationKind identifies the conceptual domain action requested.
type OperationKind string

// String returns the string representation of OperationKind.
func (k OperationKind) String() string {
	return string(k)
}

// Validate checks that the OperationKind is non-empty, bounded, and contains valid characters.
func (k OperationKind) Validate() error {
	return ValidateBoundedString(string(k), MinOperationKindLength, MaxOperationKindLength, ErrInvalidOperationKind)
}

// Operation represents an intent to perform an action against a virtual machine or control plane.
type Operation struct {
	Kind                OperationKind
	Target              MachineRef
	Actor               ActorContext
	Reason              string
	Deadline            time.Time
	IdempotencyKey      string
	RequiredCapability  string
	RequiredScopes      []string
	Classification      OperationClass
	EvidenceSensitivity EvidenceSensitivity
	Parameters          map[string]any
}

// Validate enforces fundamental invariants on the Operation.
func (op Operation) Validate() error {
	if err := op.validateHeader(); err != nil {
		return err
	}
	if err := op.validateScopesAndCaps(); err != nil {
		return err
	}
	if err := op.validateMutationFields(); err != nil {
		return err
	}
	if _, err := op.Fingerprint(); err != nil {
		return err
	}
	return nil
}

func (op Operation) validateHeader() error {
	if err := op.Kind.Validate(); err != nil {
		return err
	}
	if err := op.Target.Validate(); err != nil {
		return err
	}
	if err := op.Actor.Validate(); err != nil {
		return err
	}
	if !op.Classification.IsValid() {
		return fmt.Errorf("%w: invalid classification", ErrInvalidOperationClass)
	}
	if !op.EvidenceSensitivity.IsValid() {
		return fmt.Errorf("%w: invalid evidence sensitivity", ErrInvalidEvidenceSensitivity)
	}
	if op.Deadline.IsZero() {
		return ErrMissingDeadline
	}
	return nil
}

func (op Operation) validateScopesAndCaps() error {
	if op.RequiredCapability != "" {
		if err := ValidateCapability(op.RequiredCapability); err != nil {
			return err
		}
	}
	for _, sc := range op.RequiredScopes {
		if err := ValidateScope(sc); err != nil {
			return err
		}
	}
	return nil
}

func (op Operation) validateMutationFields() error {
	if op.Classification.IsMutation() {
		if err := ValidateIdempotencyKey(op.IdempotencyKey); err != nil {
			return err
		}
		if err := ValidateReason(op.Reason); err != nil {
			return err
		}
		return nil
	}
	if op.Reason != "" {
		if err := ValidateReason(op.Reason); err != nil {
			return err
		}
	}
	if op.IdempotencyKey != "" {
		if err := ValidateIdempotencyKey(op.IdempotencyKey); err != nil {
			return err
		}
	}
	return nil
}

// Clone returns a deep copy of the Operation, including its actor context, required scopes, and parameters.
func (op Operation) Clone() Operation {
	var scopes []string
	if op.RequiredScopes != nil {
		scopes = slices.Clone(op.RequiredScopes)
	}
	return Operation{
		Kind:                op.Kind,
		Target:              op.Target,
		Actor:               op.Actor.Clone(),
		Reason:              op.Reason,
		Deadline:            op.Deadline,
		IdempotencyKey:      op.IdempotencyKey,
		RequiredCapability:  op.RequiredCapability,
		RequiredScopes:      scopes,
		Classification:      op.Classification,
		EvidenceSensitivity: op.EvidenceSensitivity,
		Parameters:          DeepCloneMap(op.Parameters),
	}
}

// Fingerprint calculates the deterministic canonical fingerprint for this operation.
func (op Operation) Fingerprint() (Fingerprint, error) {
	return ComputeOperationFingerprint(op)
}
