package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/policy"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

var (
	// ErrMissingBackend indicates the recovery service has no backend configured.
	ErrMissingBackend = errors.New("app: recovery backend is not configured")

	// ErrAuditUnavailable indicates local audit or receipt storage is unwritable.
	ErrAuditUnavailable = errors.New("app: audit storage is unavailable or unwritable")
)

// PolicyDeniedError represents a policy evaluation refusal.
type PolicyDeniedError struct {
	Reason  policy.DenialReason
	Message string
}

func (e *PolicyDeniedError) Error() string {
	return fmt.Sprintf("policy denied (%s): %s", e.Reason, e.Message)
}

// Backend provides all hypervisor capabilities required for recovery and observation.
type Backend interface {
	MachineObserver
	Capabilities(ctx context.Context, target string) (domain.CapabilitySet, error)
	StartMachine(ctx context.Context, id string) (domain.MachineObservation, error)
	StopMachine(ctx context.Context, id string, mode string) (domain.MachineObservation, error)
	ListCheckpoints(ctx context.Context, id string) ([]domain.CheckpointObservation, error)
	CreateCheckpoint(ctx context.Context, id string, name string) (domain.CheckpointObservation, error)
	RestoreCheckpoint(ctx context.Context, id string, checkpointID string) (domain.MachineObservation, error)
}

// MutationRequest contains all required caller parameters for a state mutation.
type MutationRequest struct {
	TargetID       string
	Actor          domain.ActorContext
	Reason         string
	IdempotencyKey string
	Timeout        time.Duration
	Deadline       time.Time
	Approval       *domain.Approval
	OnAdmitted     func(ctx context.Context) error
	OnRunning      func(ctx context.Context) error
}

// RecoveryService orchestrates in-process direct recovery operations, policy, leases, and receipts.
type RecoveryService struct {
	backend       Backend
	leaseManager  *lease.Manager
	auditStore    *audit.Store
	receiptStore  *receipt.Store
	approvalStore *approval.Store
	nowFn         func() time.Time
}

// Option configures RecoveryService dependencies.
type Option func(*RecoveryService)

// WithRecoveryClock sets a custom clock function for the recovery service.
func WithRecoveryClock(fn func() time.Time) Option {
	return func(s *RecoveryService) {
		s.nowFn = fn
	}
}

// NewRecoveryService creates a new RecoveryService.
func NewRecoveryService(
	backend Backend,
	leaseManager *lease.Manager,
	auditStore *audit.Store,
	receiptStore *receipt.Store,
	approvalStore *approval.Store,
	opts ...Option,
) *RecoveryService {
	s := &RecoveryService{
		backend:       backend,
		leaseManager:  leaseManager,
		auditStore:    auditStore,
		receiptStore:  receiptStore,
		approvalStore: approvalStore,
		nowFn:         time.Now,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *RecoveryService) now() time.Time {
	if s.nowFn != nil {
		return s.nowFn().UTC()
	}
	return time.Now().UTC()
}

// ListCheckpoints returns all checkpoints for a target virtual machine.
func (s *RecoveryService) ListCheckpoints(ctx context.Context, targetID string) ([]domain.CheckpointObservation, error) {
	if s.backend == nil {
		return nil, ErrMissingBackend
	}
	if err := domain.ValidateMachineGUID(targetID); err != nil {
		return nil, err
	}
	return s.backend.ListCheckpoints(ctx, targetID)
}

// StartMachine starts a virtual machine in-process with policy and receipt verification.
func (s *RecoveryService) StartMachine(ctx context.Context, req MutationRequest) (domain.Receipt, domain.MachineObservation, error) {
	var emptyObs domain.MachineObservation
	if s.backend == nil {
		return domain.Receipt{}, emptyObs, ErrMissingBackend
	}

	op, err := s.buildOperation("machine.start", req, domain.ClassReversibleMutation, domain.CapabilityMachineStart, nil)
	if err != nil {
		return domain.Receipt{}, emptyObs, err
	}

	var obs domain.MachineObservation
	receiptRecord, execErr := s.executeMutation(ctx, op, req, func(execCtx context.Context) error {
		var runErr error
		obs, runErr = s.backend.StartMachine(execCtx, req.TargetID)
		return runErr
	})

	return receiptRecord, obs, execErr
}

// StopMachine stops a virtual machine with the specified mode (shutdown, save, turn-off).
func (s *RecoveryService) StopMachine(ctx context.Context, req MutationRequest, mode string) (domain.Receipt, domain.MachineObservation, error) {
	var emptyObs domain.MachineObservation
	if s.backend == nil {
		return domain.Receipt{}, emptyObs, ErrMissingBackend
	}

	initialClass := domain.ClassReversibleMutation
	if mode == "turn-off" {
		initialClass = domain.ClassDestructivePrivileged
	}

	params := map[string]any{"mode": mode}
	op, err := s.buildOperation("machine.stop", req, initialClass, domain.CapabilityMachineStop, params)
	if err != nil {
		return domain.Receipt{}, emptyObs, err
	}

	var obs domain.MachineObservation
	receiptRecord, execErr := s.executeMutation(ctx, op, req, func(execCtx context.Context) error {
		var runErr error
		obs, runErr = s.backend.StopMachine(execCtx, req.TargetID, mode)
		return runErr
	})

	return receiptRecord, obs, execErr
}

// CreateCheckpoint creates a new checkpoint for a virtual machine.
func (s *RecoveryService) CreateCheckpoint(ctx context.Context, req MutationRequest, name string) (domain.Receipt, domain.CheckpointObservation, error) {
	var emptyObs domain.CheckpointObservation
	if s.backend == nil {
		return domain.Receipt{}, emptyObs, ErrMissingBackend
	}
	if err := domain.ValidateBoundedString(name, 1, 256, domain.ErrInvalidCheckpointObservation); err != nil {
		return domain.Receipt{}, emptyObs, err
	}

	params := map[string]any{"name": name}
	op, err := s.buildOperation("checkpoint.create", req, domain.ClassDestructivePrivileged, domain.CapabilityCheckpointCreate, params)
	if err != nil {
		return domain.Receipt{}, emptyObs, err
	}

	var obs domain.CheckpointObservation
	receiptRecord, execErr := s.executeMutation(ctx, op, req, func(execCtx context.Context) error {
		var runErr error
		obs, runErr = s.backend.CreateCheckpoint(execCtx, req.TargetID, name)
		return runErr
	})

	return receiptRecord, obs, execErr
}

// RestoreCheckpoint restores a virtual machine to an exact checkpoint GUID.
func (s *RecoveryService) RestoreCheckpoint(ctx context.Context, req MutationRequest, checkpointID string) (domain.Receipt, domain.MachineObservation, error) {
	var emptyObs domain.MachineObservation
	if s.backend == nil {
		return domain.Receipt{}, emptyObs, ErrMissingBackend
	}
	if err := domain.ValidateMachineGUID(checkpointID); err != nil {
		return domain.Receipt{}, emptyObs, err
	}

	params := map[string]any{"checkpoint_id": checkpointID}
	op, err := s.buildOperation("checkpoint.restore", req, domain.ClassDestructivePrivileged, domain.CapabilityCheckpointRestore, params)
	if err != nil {
		return domain.Receipt{}, emptyObs, err
	}

	var obs domain.MachineObservation
	receiptRecord, execErr := s.executeMutation(ctx, op, req, func(execCtx context.Context) error {
		var runErr error
		obs, runErr = s.backend.RestoreCheckpoint(execCtx, req.TargetID, checkpointID)
		return runErr
	})

	return receiptRecord, obs, execErr
}
