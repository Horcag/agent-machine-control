// Package domain provides pure domain types, invariants, and validation rules
// for Agent Machine Control operations, actor identities, fingerprints, approvals, and receipts.
package domain

import "errors"

var (
	// Actor and delegation errors.
	ErrInvalidActorID             = errors.New("domain: invalid actor identifier")
	ErrDelegationExceedsAuthority = errors.New("domain: effective actor permissions exceed authenticated caller authority")
	ErrInvalidScope               = errors.New("domain: invalid scope identifier")

	// Operation and parameter errors.
	ErrInvalidOperationClass      = errors.New("domain: invalid operation classification")
	ErrInvalidMachineRef          = errors.New("domain: invalid target machine reference")
	ErrInvalidOperationKind       = errors.New("domain: invalid operation kind")
	ErrMissingReason              = errors.New("domain: missing mutation reason")
	ErrInvalidReason              = errors.New("domain: invalid operation reason")
	ErrMissingDeadline            = errors.New("domain: missing or zero operation deadline")
	ErrMissingIdempotencyKey      = errors.New("domain: missing idempotency key for mutation")
	ErrInvalidIdempotencyKey      = errors.New("domain: invalid idempotency key")
	ErrInvalidCapability          = errors.New("domain: invalid capability identifier")
	ErrInvalidEvidenceSensitivity = errors.New("domain: invalid evidence sensitivity")
	ErrNonCanonicalParameter      = errors.New("domain: non-canonical or unsupported parameter type/value")
	ErrInvalidFingerprint         = errors.New("domain: invalid operation fingerprint")

	// Approval errors.
	ErrInvalidApprovalRecord       = errors.New("domain: invalid approval record structure")
	ErrApprovalActorMismatch       = errors.New("domain: approval actor does not match operation effective actor")
	ErrApprovalTargetMismatch      = errors.New("domain: approval target does not match operation target")
	ErrApprovalClassMismatch       = errors.New("domain: approval authorized class does not match operation class")
	ErrApprovalFingerprintMismatch = errors.New("domain: approval fingerprint does not match operation fingerprint")
	ErrApprovalKeyMismatch         = errors.New("domain: approval idempotency key does not match operation idempotency key")
	ErrApprovalNotYetValid         = errors.New("domain: approval is not yet valid")
	ErrApprovalExpired             = errors.New("domain: approval has expired")
	ErrApprovalConsumed            = errors.New("domain: approval has already been consumed")

	// Receipt errors.
	ErrInvalidReceiptID         = errors.New("domain: invalid receipt identifier")
	ErrInvalidReceiptTimestamps = errors.New("domain: receipt completion timestamp precedes started timestamp or is zero")
	ErrMissingMutationIdentity  = errors.New("domain: mutation receipt missing idempotency key or operation fingerprint")
	ErrInvalidObservationType   = errors.New("domain: invalid observation type")
	ErrInvalidRedactionStatus   = errors.New("domain: invalid redaction status")
	ErrRedactionFailed          = errors.New("domain: receipt redaction failed")
	ErrInvalidRollbackRef       = errors.New("domain: invalid rollback reference")
	ErrInvalidEvidenceRef       = errors.New("domain: invalid evidence reference")
	ErrInvalidBackendID         = errors.New("domain: invalid backend identifier")

	// Machine observation errors.
	ErrInvalidMachineID            = errors.New("domain: invalid machine identifier/GUID")
	ErrInvalidMachineName          = errors.New("domain: invalid machine display name")
	ErrInvalidLifecycleState       = errors.New("domain: invalid machine lifecycle state")
	ErrInvalidMetricValue          = errors.New("domain: metric value cannot be negative")
	ErrDuplicateMachineID          = errors.New("domain: duplicate machine identifier in observation set")
	ErrInvalidNetworkAdapter       = errors.New("domain: invalid network adapter observation")
	ErrInvalidObservationTimestamp = errors.New("domain: machine observation timestamp cannot be zero")

	// Checkpoint observation errors.
	ErrInvalidCheckpointID          = errors.New("domain: invalid checkpoint identifier/GUID")
	ErrInvalidCheckpointObservation = errors.New("domain: invalid checkpoint observation")

	// Operation lifecycle and event errors.
	ErrInvalidOperationState  = errors.New("domain: invalid operation state")
	ErrIllegalStateTransition = errors.New("domain: illegal operation state transition")
	ErrInvalidOperationID     = errors.New("domain: invalid operation identifier")
	ErrInvalidEventSequence   = errors.New("domain: invalid event sequence")

	// Session errors.
	ErrInvalidSessionID          = errors.New("domain: invalid session identifier")
	ErrSessionNotFound           = errors.New("domain: session not found")
	ErrSessionClosed             = errors.New("domain: session is closed")
	ErrSessionOpening            = errors.New("domain: session is still opening")
	ErrSessionWriteBusy          = errors.New("domain: concurrent session write in progress")
	ErrInvalidControlKey         = errors.New("domain: invalid terminal control key")
	ErrInvalidTerminalDimensions = errors.New("domain: terminal dimensions out of bounds")
	ErrInvalidTerminalType       = errors.New("domain: invalid terminal type")
	ErrInvalidSessionObservation = errors.New("domain: invalid session observation")
	ErrSessionWaitTimeout        = errors.New("domain: wait condition timed out before settling or matching pattern")
	ErrSessionAccessDenied       = errors.New("domain: session access denied")
	ErrSessionConflict           = errors.New("domain: session conflict")
	ErrSessionManagerClosed      = errors.New("domain: session manager is closed")
	ErrHostKeyMismatch           = errors.New("domain: host key mismatch")
	ErrMissingHostKeyPin         = errors.New("domain: missing pinned host key")
)
