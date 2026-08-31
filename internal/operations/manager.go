package operations

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/events"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

// Manager manages in-flight admission, lifecycle persistence, and execution of operations.
type Manager struct {
	dir             string
	recoveryService *app.RecoveryService
	receiptStore    *receipt.Store
	auditStore      *audit.Store
	eventHub        *events.Hub
	nowFn           func() time.Time

	closing      atomic.Bool
	liveOpsCount atomic.Int64
	capacity     chan struct{}
	wg           sync.WaitGroup
	drainOnce    sync.Once
	drained      chan struct{}

	mu                 sync.Mutex
	inFlight           map[string]*inFlightEntry // keyed globally by IdempotencyKey
	liveCancels        map[string]context.CancelCauseFunc
	finalizationErrors map[string]error
}

// Option configures Manager options.
type Option func(*Manager)

// WithClock sets a custom clock function for the operation manager.
func WithClock(fn func() time.Time) Option {
	return func(m *Manager) {
		m.nowFn = fn
	}
}

// NewManager creates a new operation Manager.
func NewManager(
	dir string,
	recoverySvc *app.RecoveryService,
	rcptStore *receipt.Store,
	auditStore *audit.Store,
	eventHub *events.Hub,
	opts ...Option,
) *Manager {
	m := &Manager{
		dir:                dir,
		recoveryService:    recoverySvc,
		receiptStore:       rcptStore,
		auditStore:         auditStore,
		eventHub:           eventHub,
		nowFn:              time.Now,
		inFlight:           make(map[string]*inFlightEntry),
		liveCancels:        make(map[string]context.CancelCauseFunc),
		finalizationErrors: make(map[string]error),
		capacity:           make(chan struct{}, 100),
		drained:            make(chan struct{}),
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

func (m *Manager) now() time.Time {
	if m.nowFn != nil {
		return m.nowFn().UTC()
	}
	return time.Now().UTC()
}

func validateSubmission(op domain.Operation) (domain.Fingerprint, error) {
	if err := op.Validate(); err != nil {
		return "", err
	}
	if err := domain.ValidateOperationParameters(op.Kind, op.Parameters); err != nil {
		return "", err
	}
	return op.Fingerprint()
}

// Submit submits an operation for execution. Exact in-flight or completed retries are attached/returned.
func (m *Manager) Submit(ctx context.Context, op domain.Operation, timeout time.Duration) (*domain.OperationRecord, bool, error) {
	return m.submit(ctx, op, timeout, "", nil)
}

// SubmitWithApprovalReference submits an operation and resolves authority only after exact retries are checked.
func (m *Manager) SubmitWithApprovalReference(ctx context.Context, op domain.Operation, timeout time.Duration, approvalID string, resolver ApprovalResolver) (*domain.OperationRecord, bool, error) {
	return m.submit(ctx, op, timeout, domain.ApprovalID(approvalID), resolver)
}

func (m *Manager) submit(ctx context.Context, op domain.Operation, timeout time.Duration, approvalID domain.ApprovalID, resolver ApprovalResolver) (*domain.OperationRecord, bool, error) {
	if m.closing.Load() {
		return nil, false, ErrManagerShuttingDown
	}
	fp, err := validateSubmission(op)
	if err != nil {
		return nil, false, err
	}
	if err := validateApprovalResolver(approvalID, resolver); err != nil {
		return nil, false, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closing.Load() {
		return nil, false, ErrManagerShuttingDown
	}
	// 1. Check in-flight and on-disk idempotency
	if op.IdempotencyKey != "" {
		if rec, found, matchErr := m.checkInFlightAndDisk(op, fp, approvalID); found || matchErr != nil {
			return rec, found, matchErr
		}
		if rec, found, matchErr := m.checkCachedReceipt(op, approvalID); found || matchErr != nil {
			return rec, found, matchErr
		}
	}
	if !m.reserveCapacity() {
		return nil, false, ErrManagerBusy
	}

	resolvedApproval, approvalErr, fatalErr := resolveSubmissionApproval(ctx, approvalID, resolver)
	if fatalErr != nil {
		<-m.capacity
		return nil, false, fatalErr
	}

	rec, execCtx, stopDeadline, err := m.initializeNewRecord(ctx, op, fp, approvalID)
	if err != nil {
		<-m.capacity
		return nil, false, err
	}

	// Launch async execution
	returnCopy := rec.Clone()
	go m.executeOperation(execCtx, stopDeadline, returnCopy, op, timeout, resolvedApproval, approvalErr)

	return &returnCopy, false, nil
}

func validateApprovalResolver(approvalID domain.ApprovalID, resolver ApprovalResolver) error {
	if approvalID == "" {
		return nil
	}
	if err := approvalID.Validate(); err != nil {
		return err
	}
	if resolver == nil {
		return domain.ErrInvalidApprovalRecord
	}
	return nil
}

func (m *Manager) reserveCapacity() bool {
	select {
	case m.capacity <- struct{}{}:
		return true
	default:
		return false
	}
}

func resolveSubmissionApproval(ctx context.Context, approvalID domain.ApprovalID, resolver ApprovalResolver) (*domain.Approval, error, error) {
	if approvalID == "" {
		return nil, nil, nil
	}
	resolved, resolutionErr := resolver(ctx)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, nil, ctxErr
	}
	if resolved != nil && resolved.ID != approvalID {
		return nil, app.ErrInvalidOperationApprovalReference, nil
	}
	return resolved, resolutionErr, nil
}

func (m *Manager) initializeNewRecord(ctx context.Context, op domain.Operation, fp domain.Fingerprint, approvalID domain.ApprovalID) (*domain.OperationRecord, context.Context, context.CancelFunc, error) {
	opID, err := generateOpID()
	if err != nil {
		return nil, nil, nil, err
	}

	idFp, err := domain.ComputeIdempotencyFingerprint(op)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("operations: failed to compute idempotency fingerprint: %w", err)
	}

	now := m.now()
	rec := &domain.OperationRecord{
		SchemaVersion:          "1",
		ID:                     opID,
		Actor:                  op.Actor.EffectiveActor,
		Target:                 op.Target,
		Kind:                   op.Kind,
		RequestedClass:         op.Classification,
		EffectiveClass:         op.Classification,
		Fingerprint:            fp,
		IdempotencyFingerprint: idFp,
		IdempotencyKey:         op.IdempotencyKey,
		ApprovalID:             approvalID,
		Deadline:               op.Deadline,
		State:                  domain.OpStatePending,
		CreatedAt:              now,
		Parameters:             domain.DeepCloneMap(op.Parameters),
	}

	if err := SaveRecord(m.dir, *rec); err != nil {
		return nil, nil, nil, err
	}

	if m.eventHub != nil {
		if _, err := m.eventHub.Publish(ctx, events.Event{
			OperationID: opID,
			Target:      op.Target,
			EventType:   "state_change",
			State:       domain.OpStatePending,
		}); err != nil {
			_ = os.Remove(filepath.Join(m.dir, fmt.Sprintf("%s.json", opID)))
			return nil, nil, nil, fmt.Errorf("operations: failed to publish initial pending event: %w", err)
		}
	}

	executionDeadline := op.Deadline
	if approvalID != "" && !executionDeadline.After(now) {
		executionDeadline = now.Add(5 * time.Second)
	}
	deadlineCtx, stopDeadline := context.WithDeadlineCause(context.Background(), executionDeadline, context.DeadlineExceeded)
	execCtx, cancelFunc := context.WithCancelCause(deadlineCtx)
	m.liveCancels[opID] = cancelFunc

	if op.IdempotencyKey != "" {
		m.inFlight[op.IdempotencyKey] = &inFlightEntry{
			opID:                   opID,
			fingerprint:            fp,
			idempotencyFingerprint: idFp,
			target:                 op.Target,
			kind:                   op.Kind,
			actor:                  op.Actor.EffectiveActor,
			approvalID:             approvalID,
			record:                 rec,
		}
	}

	m.liveOpsCount.Add(1)
	m.wg.Add(1)

	return rec, execCtx, stopDeadline, nil
}

func isOperator(caller domain.ActorContext) bool {
	return caller.CallerPermissions.Has("audit:read") || caller.CallerPermissions.Has("operation:cancel")
}

// Shutdown gracefully cancels all active in-flight operations and waits for drain.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.closing.Store(true)

	m.mu.Lock()
	for _, cancelFn := range m.liveCancels {
		if cancelFn != nil {
			cancelFn(errors.New("daemon shutting down"))
		}
	}
	m.mu.Unlock()

	m.drainOnce.Do(func() {
		go func() {
			m.wg.Wait()
			close(m.drained)
		}()
	})

	var errs []error
	select {
	case <-m.drained:
	case <-ctx.Done():
		errs = append(errs, fmt.Errorf("operations: shutdown timed out waiting for %d live operations: %w", m.liveOpsCount.Load(), ctx.Err()))
	}

	m.mu.Lock()
	for _, finErr := range m.finalizationErrors {
		errs = append(errs, finErr)
	}
	m.mu.Unlock()

	return errors.Join(errs...)
}

// Drained reports whether every admitted operation has finished execution and finalization.
// Terminal finalization errors remain reportable but do not represent live owned work.
func (m *Manager) Drained() bool {
	return m == nil || m.liveOpsCount.Load() == 0
}

// Get returns the operation record for a given ID, checking permissions and in-memory freshness.
func (m *Manager) Get(opID string, caller domain.ActorContext) (*domain.OperationRecord, error) {
	if err := domain.ValidateOperationID(opID); err != nil {
		return nil, err
	}

	m.mu.Lock()
	for _, entry := range m.inFlight {
		if entry.opID == opID {
			recCopy := entry.record.Clone()
			m.mu.Unlock()
			if !isOperator(caller) && recCopy.Actor != caller.EffectiveActor {
				return nil, ErrOperationNotFound
			}
			return &recCopy, nil
		}
	}
	finErr, hasFinErr := m.finalizationErrors[opID]
	m.mu.Unlock()

	rec, err := ReadRecord(m.dir, opID)
	if err != nil {
		return nil, err
	}

	if !isOperator(caller) && rec.Actor != caller.EffectiveActor {
		return nil, ErrOperationNotFound
	}

	if hasFinErr && finErr != nil {
		rec.State = domain.OpStateFailed
		rec.ErrorCategory = "finalization_error"
		rec.ErrorMessage = finErr.Error()
	}

	return rec, nil
}

// List returns a list of operations matching options, checking permissions.
func (m *Manager) List(opts ListOptions, caller domain.ActorContext) ([]domain.OperationRecord, error) {
	records, err := ListRecords(m.dir, opts)
	if err != nil {
		return nil, err
	}

	filtered := make([]domain.OperationRecord, 0, len(records))
	for _, r := range records {
		if isOperator(caller) || r.Actor == caller.EffectiveActor {
			filtered = append(filtered, r)
		}
	}
	return filtered, nil
}

// Cancel cancels a running or pending operation.
func (m *Manager) Cancel(opID string, caller domain.ActorContext, _ string) error {
	if err := domain.ValidateOperationID(opID); err != nil {
		return err
	}

	rec, err := ReadRecord(m.dir, opID)
	if err != nil {
		return err
	}

	if !isOperator(caller) && rec.Actor != caller.EffectiveActor {
		return ErrOperationNotFound
	}

	if rec.State.IsTerminal() {
		return ErrOperationTerminal
	}

	sanitizedReason := "cancelled by operator"
	if !isOperator(caller) {
		sanitizedReason = "cancelled by client"
	}

	m.mu.Lock()
	cancelFn, ok := m.liveCancels[opID]
	m.mu.Unlock()

	if ok && cancelFn != nil {
		cancelFn(errors.New(sanitizedReason))
		return nil
	}

	// Operation not live in memory (dead process): update directly and record synthetic receipt
	now := m.now()
	rec.State = domain.OpStateCancelled
	rec.ErrorCategory = "cancelled"
	rec.ErrorMessage = sanitizedReason
	rec.CompletedAt = now

	rcptID, err := ensureSyntheticReceipt(rec, m.receiptStore, m.auditStore, now)
	if err != nil {
		return fmt.Errorf("operations: failed to create cancellation receipt: %w", err)
	}

	if err := SaveRecord(m.dir, *rec); err != nil {
		return fmt.Errorf("operations: failed to persist cancelled record: %w", err)
	}

	if m.eventHub != nil {
		if _, err := m.eventHub.Publish(context.Background(), events.Event{
			OperationID: opID,
			Target:      rec.Target,
			EventType:   "terminal",
			State:       domain.OpStateCancelled,
			Category:    "cancelled",
			Message:     sanitizedReason,
			ReceiptID:   domain.ReceiptID(rcptID),
		}); err != nil {
			return fmt.Errorf("operations: failed to publish cancellation event: %w", err)
		}
	}

	return nil
}

func matchIdempotency(actual, recFp, opFp, opIDFp domain.Fingerprint) bool {
	if actual == "" {
		return recFp == opFp
	}
	return actual == opIDFp
}

func (m *Manager) checkInFlightAndDisk(op domain.Operation, fp domain.Fingerprint, approvalID domain.ApprovalID) (*domain.OperationRecord, bool, error) {
	idFp, err := domain.ComputeIdempotencyFingerprint(op)
	if err != nil {
		return nil, false, err
	}
	if existing, ok := m.inFlight[op.IdempotencyKey]; ok {
		if inFlightMatches(existing, op, fp, idFp, approvalID) {
			recCopy := existing.record.Clone()
			return &recCopy, true, nil
		}
		return nil, false, ErrOperationConflict
	}

	existingRecords, err := ListRecords(m.dir, ListOptions{})
	if err != nil {
		return nil, false, fmt.Errorf("operations: failed to list records for idempotency check: %w", err)
	}

	for _, rec := range existingRecords {
		if rec.IdempotencyKey == op.IdempotencyKey {
			if operationRecordMatches(rec, op, fp, idFp, approvalID) {
				recCopy := rec.Clone()
				return &recCopy, true, nil
			}
			return nil, false, ErrOperationConflict
		}
	}
	return nil, false, nil
}

func inFlightMatches(existing *inFlightEntry, op domain.Operation, fp, idFp domain.Fingerprint, approvalID domain.ApprovalID) bool {
	return matchIdempotency(existing.idempotencyFingerprint, existing.fingerprint, fp, idFp) &&
		existing.target == op.Target && existing.kind == op.Kind && existing.actor == op.Actor.EffectiveActor && existing.approvalID == approvalID
}

func operationRecordMatches(rec domain.OperationRecord, op domain.Operation, fp, idFp domain.Fingerprint, approvalID domain.ApprovalID) bool {
	return matchIdempotency(rec.IdempotencyFingerprint, rec.Fingerprint, fp, idFp) &&
		rec.Target == op.Target && rec.Kind == op.Kind && rec.Actor == op.Actor.EffectiveActor && rec.ApprovalID == approvalID
}

func generateOpID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto/rand failed: %w", err)
	}
	return fmt.Sprintf("op-%s", hex.EncodeToString(b)), nil
}
