package operations

import (
	"errors"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

var (
	// ErrOperationNotFound indicates an operation ID was not found or inaccessible.
	ErrOperationNotFound = errors.New("operations: operation not found")

	// ErrOperationConflict indicates an idempotency collision with different parameters or target.
	ErrOperationConflict = errors.New("operations: idempotency collision with existing operation")

	// ErrOperationUnauthorized indicates the caller is not authorized to access or cancel this operation.
	ErrOperationUnauthorized = errors.New("operations: unauthorized access to operation")

	// ErrOperationTerminal indicates the operation has already completed and cannot be cancelled.
	ErrOperationTerminal = errors.New("operations: operation is already in a terminal state")

	// ErrManagerShuttingDown indicates the operations manager is draining/shutting down and rejecting new submissions.
	ErrManagerShuttingDown = errors.New("operations: manager is shutting down")

	// ErrManagerBusy indicates live operation capacity has been reached.
	ErrManagerBusy = errors.New("operations: live operation capacity exceeded")

	// ErrPersistenceFailure indicates a storage error or that persistence is not configured properly.
	ErrPersistenceFailure = errors.New("operations: persistent storage is not configured")
)

// ListOptions specifies filter and pagination parameters for querying operations.
type ListOptions struct {
	State   domain.OperationState
	Machine domain.MachineRef
	Limit   int
}

// InFlightKey uniquely identifies a submitted mutation per actor and idempotency key.
type inFlightEntry struct {
	opID                   string
	fingerprint            domain.Fingerprint
	idempotencyFingerprint domain.Fingerprint
	target                 domain.MachineRef
	kind                   domain.OperationKind
	actor                  domain.ActorID
	record                 *domain.OperationRecord
}
