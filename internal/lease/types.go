package lease

import (
	"errors"
	"time"
)

const (
	// SchemaVersion is the canonical lease format version.
	SchemaVersion = "1"

	// DefaultLeaseTTL is the default duration for which a lease is held.
	DefaultLeaseTTL = 30 * time.Second
)

var (
	// ErrLeaseConflict indicates an active lease is held by another process/owner.
	ErrLeaseConflict = errors.New("lease: machine lease is held by another active process or conflicting owner")

	// ErrLeaseUnverifiableOwner indicates an expired lease cannot be reclaimed because the owner is in a different or unverifiable runtime.
	ErrLeaseUnverifiableOwner = errors.New("lease: expired lease cannot be reclaimed because owner runtime is different or unverifiable")

	// ErrLeaseFencingViolation indicates the release request does not match the active lease's owner or fencing generation.
	ErrLeaseFencingViolation = errors.New("lease: fencing generation or owner mismatch during lease release")

	// ErrInvalidLeaseData indicates the lease file is corrupted, malformed, or has an unsupported schema version.
	ErrInvalidLeaseData = errors.New("lease: invalid or corrupt lease data")
)

// Lease represents a persistent, host-visible lock on a virtual machine.
type Lease struct {
	SchemaVersion     string    `json:"schema_version"`
	MachineID         string    `json:"machine_id"`
	OwnerID           string    `json:"owner_id"`
	RuntimeID         string    `json:"runtime_id"`
	PID               int       `json:"pid"`
	ProcessStartTime  string    `json:"process_start_time,omitempty"`
	OperationKind     string    `json:"operation_kind"`
	Fingerprint       string    `json:"fingerprint"`
	AcquiredAt        time.Time `json:"acquired_at"`
	ExpiresAt         time.Time `json:"expires_at"`
	FencingGeneration uint64    `json:"fencing_generation"`
}

// LockOwnerRecord stores process identity for transition lock ownership.
type LockOwnerRecord struct {
	SchemaVersion    string    `json:"schema_version"`
	RuntimeID        string    `json:"runtime_id"`
	PID              int       `json:"pid"`
	ProcessStartTime string    `json:"process_start_time,omitempty"`
	AcquiredAt       time.Time `json:"acquired_at"`
}

// LivenessChecker verifies whether a process is still alive in the current runtime.
type LivenessChecker interface {
	IsAlive(pid int, startTime string) (bool, error)
}

// IdentityProvider returns the current runtime and process identity.
type IdentityProvider interface {
	CurrentIdentity() (runtimeID string, pid int, startTime string)
}
