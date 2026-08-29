package domain

import (
	"fmt"
)

// OperationClass defines the safety classification of an operation.
type OperationClass string

const (
	// ClassObserve represents read-only operations with no mutating side effects.
	ClassObserve OperationClass = "observe"

	// ClassReversibleMutation represents state changes where a verified rollback checkpoint exists.
	ClassReversibleMutation OperationClass = "reversible_mutation"

	// ClassDestructivePrivileged represents irreversible, uncheckpointed, host-modifying, or privileged mutations.
	ClassDestructivePrivileged OperationClass = "destructive_privileged"

	// ClassForbidden represents operations that violate fundamental security invariants.
	ClassForbidden OperationClass = "forbidden"
)

// IsValid returns true if the OperationClass is one of the four defined classes.
func (c OperationClass) IsValid() bool {
	switch c {
	case ClassObserve, ClassReversibleMutation, ClassDestructivePrivileged, ClassForbidden:
		return true
	default:
		return false
	}
}

// IsMutation returns true if the operation modifies host or guest state.
func (c OperationClass) IsMutation() bool {
	return c == ClassReversibleMutation || c == ClassDestructivePrivileged
}

// RequiresApproval returns true if the operation inherently requires an active operator approval.
func (c OperationClass) RequiresApproval() bool {
	return c == ClassDestructivePrivileged
}

// String returns the string representation of the classification.
func (c OperationClass) String() string {
	return string(c)
}

// ParseOperationClass parses and validates an operation class string.
func ParseOperationClass(s string) (OperationClass, error) {
	class := OperationClass(s)
	if !class.IsValid() {
		return "", fmt.Errorf("%w: unrecognized class", ErrInvalidOperationClass)
	}
	return class, nil
}

// EvidenceSensitivity indicates whether an operation collects sensitive evidence.
type EvidenceSensitivity string

const (
	// EvidenceSensitivityUnspecified represents default / unspecified evidence sensitivity.
	EvidenceSensitivityUnspecified EvidenceSensitivity = ""
	// EvidenceSensitivityStandard indicates standard non-sensitive operation evidence.
	EvidenceSensitivityStandard EvidenceSensitivity = "standard"
	// EvidenceSensitivitySensitive indicates sensitive evidence capture (e.g. framebuffer/screenshots).
	EvidenceSensitivitySensitive EvidenceSensitivity = "sensitive"
)

// IsValid checks if the EvidenceSensitivity is valid.
func (e EvidenceSensitivity) IsValid() bool {
	switch e {
	case EvidenceSensitivityUnspecified, EvidenceSensitivityStandard, EvidenceSensitivitySensitive:
		return true
	default:
		return false
	}
}

// IsSensitive returns true if the evidence sensitivity is sensitive.
func (e EvidenceSensitivity) IsSensitive() bool {
	return e == EvidenceSensitivitySensitive
}

// String returns the string representation of EvidenceSensitivity.
func (e EvidenceSensitivity) String() string {
	return string(e)
}
