package domain

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// ReceiptID uniquely identifies an execution receipt.
type ReceiptID string

// String returns the string representation of ReceiptID.
func (id ReceiptID) String() string {
	return string(id)
}

// Validate checks that the ReceiptID is a valid canonical identifier.
func (id ReceiptID) Validate() error {
	return ValidateReceiptID(string(id))
}

// GenerateReceiptID creates a new canonical ReceiptID (rcpt-<32 hex chars>).
func GenerateReceiptID() (ReceiptID, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate receipt id: %w", err)
	}
	return ReceiptID(fmt.Sprintf("rcpt-%s", hex.EncodeToString(b))), nil
}

// ObservationType distinguishes verified direct observations from inferred assertions.
type ObservationType string

const (
	// ObservationObserved indicates state measured directly from hypervisor/OS interfaces.
	ObservationObserved ObservationType = "observed"
	// ObservationInferred indicates state deduced from secondary indicators or heuristics.
	ObservationInferred ObservationType = "inferred"
)

// IsValid checks if the ObservationType is recognized.
func (o ObservationType) IsValid() bool {
	return o == ObservationObserved || o == ObservationInferred
}

// RedactionStatus indicates whether the redaction pipeline sanitized the receipt.
type RedactionStatus string

const (
	// RedactionApplied indicates sensitive streams/text were processed and sanitized.
	RedactionApplied RedactionStatus = "applied"
	// RedactionNotApplicable indicates no sensitive or secret-bearing fields were present.
	RedactionNotApplicable RedactionStatus = "not_applicable"
	// RedactionFailed indicates redaction failed and the receipt must not be emitted.
	RedactionFailed RedactionStatus = "failed"
)

// IsValid checks if the RedactionStatus is recognized.
func (r RedactionStatus) IsValid() bool {
	return r == RedactionApplied || r == RedactionNotApplicable || r == RedactionFailed
}

// OutcomeStatus represents the terminal execution state of an operation.
type OutcomeStatus string

const (
	// OutcomeSuccess indicates the operation succeeded.
	OutcomeSuccess OutcomeStatus = "success"
	// OutcomeFailed indicates execution encountered an error.
	OutcomeFailed OutcomeStatus = "failed"
	// OutcomeAborted indicates execution was cancelled or aborted before completion.
	OutcomeAborted OutcomeStatus = "aborted"
	// OutcomeDenied indicates the operation was denied by policy.
	OutcomeDenied OutcomeStatus = "denied"
)

// IsValid checks if the OutcomeStatus is recognized.
func (os OutcomeStatus) IsValid() bool {
	return os == OutcomeSuccess || os == OutcomeFailed || os == OutcomeAborted || os == OutcomeDenied
}

// ExecutionOutcome records the exit outcome of an executed operation without raw free-form text.
type ExecutionOutcome struct {
	Status        OutcomeStatus
	ExitCode      int
	ErrorCategory string
	ErrorMessage  string
}

// Receipt contains the tamper-evident record of an executed operation.
type Receipt struct {
	ReceiptID              ReceiptID
	OperationKind          OperationKind
	Fingerprint            Fingerprint
	IdempotencyFingerprint Fingerprint
	IdempotencyKey         string
	Actor                  ActorID
	Target                 MachineRef
	Class                  OperationClass
	EffectiveBackend       string
	StartedAt              time.Time
	CompletedAt            time.Time
	Outcome                ExecutionOutcome
	ObservationType        ObservationType
	EvidenceRefs           []string
	RollbackRef            string
	RedactionStatus        RedactionStatus
}

// Validate verifies all security and integrity invariants of the receipt.
func (r Receipt) Validate() error {
	if err := r.validateHeader(); err != nil {
		return err
	}
	if err := r.validateTimestamps(); err != nil {
		return err
	}
	if err := r.validateOperationIdentity(); err != nil {
		return err
	}
	if err := r.validateEvidenceRefs(); err != nil {
		return err
	}
	return nil
}

func (r Receipt) validateHeader() error {
	if err := r.ReceiptID.Validate(); err != nil {
		return err
	}
	if err := r.OperationKind.Validate(); err != nil {
		return err
	}
	if err := r.Actor.Validate(); err != nil {
		return err
	}
	if err := r.Target.Validate(); err != nil {
		return err
	}
	if !r.Class.IsValid() {
		return fmt.Errorf("%w: invalid operation class", ErrInvalidOperationClass)
	}
	if err := ValidateBackendID(r.EffectiveBackend); err != nil {
		return err
	}
	if !r.Outcome.Status.IsValid() {
		return fmt.Errorf("%w: invalid outcome status", ErrInvalidReceiptID)
	}
	if !r.ObservationType.IsValid() {
		return fmt.Errorf("%w: invalid observation type", ErrInvalidObservationType)
	}
	if !r.RedactionStatus.IsValid() {
		return fmt.Errorf("%w: invalid redaction status", ErrInvalidRedactionStatus)
	}
	if r.RedactionStatus == RedactionFailed {
		return ErrRedactionFailed
	}
	if err := r.validateOutcomeFields(); err != nil {
		return err
	}
	return nil
}

func (r Receipt) validateOutcomeFields() error {
	switch r.Outcome.Status {
	case OutcomeDenied:
		return validateDeniedOutcome(r.Outcome)
	case OutcomeAborted:
		return validateAbortedOutcome(r.Outcome)
	case OutcomeFailed:
		return validateFailedOutcome(r.Outcome)
	default:
		if r.Outcome.ErrorCategory != "" || r.Outcome.ErrorMessage != "" {
			return fmt.Errorf("%w: error category and message must be empty for non-denied outcomes", ErrInvalidReceiptID)
		}
	}
	return nil
}

func validateDeniedOutcome(outcome ExecutionOutcome) error {
	if err := ValidateBoundedString(outcome.ErrorCategory, 1, 256, ErrInvalidReceiptID); err != nil {
		return fmt.Errorf("invalid error category: %w", err)
	}
	if err := ValidateBoundedString(outcome.ErrorMessage, 1, 1024, ErrInvalidReceiptID); err != nil {
		return fmt.Errorf("invalid error message: %w", err)
	}
	return nil
}

func validateAbortedOutcome(outcome ExecutionOutcome) error {
	if outcome.ErrorCategory == "" && outcome.ErrorMessage == "" {
		return nil
	}
	if outcome.ErrorCategory != FailureCategoryCallerCanceled && outcome.ErrorCategory != FailureCategoryDeadlineExceeded {
		return fmt.Errorf("%w: aborted outcome requires canonical cancellation provenance", ErrInvalidReceiptID)
	}
	if err := ValidateBoundedString(outcome.ErrorMessage, 1, 1024, ErrInvalidReceiptID); err != nil {
		return fmt.Errorf("invalid aborted outcome message: %w", err)
	}
	return nil
}

func validateFailedOutcome(outcome ExecutionOutcome) error {
	if outcome.ErrorCategory == "" && outcome.ErrorMessage == "" {
		return nil
	}
	message, ok := CanonicalFailureMessage(outcome.ErrorCategory)
	if !ok || outcome.ErrorMessage != message {
		return fmt.Errorf("%w: failed outcome requires canonical failure provenance", ErrInvalidReceiptID)
	}
	return nil
}

func (r Receipt) validateTimestamps() error {
	if r.StartedAt.IsZero() || r.CompletedAt.IsZero() || r.CompletedAt.Before(r.StartedAt) {
		return ErrInvalidReceiptTimestamps
	}
	return nil
}

func (r Receipt) validateOperationIdentity() error {
	// Every receipt (observe and mutation) requires a valid operation fingerprint.
	if err := r.Fingerprint.Validate(); err != nil {
		return fmt.Errorf("%w: invalid fingerprint in receipt", ErrMissingMutationIdentity)
	}
	if r.IdempotencyFingerprint != "" {
		if err := r.IdempotencyFingerprint.Validate(); err != nil {
			return fmt.Errorf("%w: invalid idempotency fingerprint in receipt", ErrMissingMutationIdentity)
		}
	}

	if r.Class.IsMutation() {
		if err := ValidateIdempotencyKey(r.IdempotencyKey); err != nil {
			return fmt.Errorf("%w: missing or invalid idempotency key in mutation receipt", ErrMissingMutationIdentity)
		}
		if r.Class == ClassReversibleMutation && r.Outcome.Status == OutcomeSuccess {
			if err := ValidateRollbackRef(r.RollbackRef); err != nil {
				return fmt.Errorf("%w: successful reversible mutation receipt missing valid rollback reference", ErrMissingMutationIdentity)
			}
		}
	}

	if r.RollbackRef != "" {
		if err := ValidateRollbackRef(r.RollbackRef); err != nil {
			return fmt.Errorf("%w: invalid rollback reference", ErrInvalidReceiptID)
		}
	}

	return nil
}

func (r Receipt) validateEvidenceRefs() error {
	for _, ref := range r.EvidenceRefs {
		if err := ValidateEvidenceRef(ref); err != nil {
			return err
		}
	}
	return nil
}
