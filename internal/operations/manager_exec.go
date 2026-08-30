package operations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/events"
)

func (m *Manager) executeOperation(ctx context.Context, stopDeadline context.CancelFunc, rec domain.OperationRecord, op domain.Operation, timeout time.Duration) {
	defer stopDeadline()
	defer m.wg.Done()
	defer m.liveOpsCount.Add(-1)
	defer func() { <-m.capacity }()
	defer func() {
		m.mu.Lock()
		delete(m.liveCancels, rec.ID)
		if op.IdempotencyKey != "" {
			delete(m.inFlight, op.IdempotencyKey)
		}
		m.mu.Unlock()
	}()

	// Dispatch to RecoveryService with OnAdmitted and OnRunning hooks
	req := app.MutationRequest{
		TargetID:       string(op.Target),
		Actor:          op.Actor,
		Reason:         op.Reason,
		IdempotencyKey: op.IdempotencyKey,
		Timeout:        timeout,
		Deadline:       op.Deadline,
		OnAdmitted: func(execCtx context.Context) error {
			return m.saveAndPublishState(execCtx, &rec, domain.OpStateAdmitted)
		},
		OnRunning: func(execCtx context.Context) error {
			return m.saveAndPublishState(execCtx, &rec, domain.OpStateRunning)
		},
	}

	rcpt, execErr := m.dispatchBackend(ctx, op, req)

	_ = m.finalizeOperationState(ctx, &rec, rcpt, execErr)
}

func (m *Manager) saveAndPublishState(execCtx context.Context, rec *domain.OperationRecord, state domain.OperationState) error {
	now := m.now()
	rec.State = state
	switch state {
	case domain.OpStateAdmitted:
		rec.AdmittedAt = now
	case domain.OpStateRunning:
		rec.RunningAt = now
	}

	if err := SaveRecord(m.dir, *rec); err != nil {
		return fmt.Errorf("operations: failed to save %s record: %w", state, err)
	}

	m.mu.Lock()
	for _, entry := range m.inFlight {
		if entry.opID == rec.ID {
			cloned := rec.Clone()
			entry.record = &cloned
			break
		}
	}
	m.mu.Unlock()

	if m.eventHub != nil {
		if _, err := m.eventHub.Publish(execCtx, events.Event{
			OperationID: rec.ID,
			Target:      rec.Target,
			EventType:   "state_change",
			State:       state,
		}); err != nil {
			return fmt.Errorf("operations: failed to publish %s event: %w", state, err)
		}
	}
	return nil
}

func (m *Manager) finalizeOperationState(ctx context.Context, rec *domain.OperationRecord, rcpt domain.Receipt, execErr error) error {
	now := m.now()
	rec.CompletedAt = now

	switch {
	case ctx.Err() != nil:
		if errors.Is(ctx.Err(), context.Canceled) {
			rec.State = domain.OpStateCancelled
			rec.ErrorCategory = "cancelled"
			cause := context.Cause(ctx)
			if cause != nil {
				rec.ErrorMessage = cause.Error()
			} else {
				rec.ErrorMessage = "operation cancelled"
			}
		} else {
			rec.State = domain.OpStateFailed
			rec.ErrorCategory = "timeout"
			rec.ErrorMessage = "operation deadline exceeded"
		}
		if rcpt.ReceiptID != "" {
			rec.ReceiptID = rcpt.ReceiptID
		}
	case execErr != nil:
		rec.State = domain.OpStateFailed
		rec.ErrorCategory, rec.ErrorMessage = sanitizeExecError(execErr)
		if rcpt.ReceiptID != "" {
			rec.ReceiptID = rcpt.ReceiptID
		}
	default:
		rec.State = domain.OpStateCompleted
		rec.ReceiptID = rcpt.ReceiptID
	}

	m.mu.Lock()
	for _, entry := range m.inFlight {
		if entry.opID == rec.ID {
			cloned := rec.Clone()
			entry.record = &cloned
			break
		}
	}
	m.mu.Unlock()

	var finalizationErrs []error

	if saveErr := SaveRecord(m.dir, *rec); saveErr != nil {
		finalizationErrs = append(finalizationErrs, fmt.Errorf("operations: failed to save terminal record: %w", saveErr))
		rec.State = domain.OpStateFailed
		rec.ErrorCategory = "persistence_failure"
		rec.ErrorMessage = saveErr.Error()
	}

	if m.eventHub != nil {
		termEv := events.Event{
			OperationID: rec.ID,
			Target:      rec.Target,
			EventType:   "terminal",
			State:       rec.State,
			Category:    rec.ErrorCategory,
			Message:     rec.ErrorMessage,
			ReceiptID:   rec.ReceiptID,
		}
		if _, pubErr := m.eventHub.Publish(context.Background(), termEv); pubErr != nil {
			m.eventHub.BroadcastEphemeral(termEv)
			finalizationErrs = append(finalizationErrs, fmt.Errorf("operations: failed to publish terminal event: %w", pubErr))
		}
	}

	if len(finalizationErrs) > 0 {
		joined := errors.Join(finalizationErrs...)
		m.mu.Lock()
		m.finalizationErrors[rec.ID] = joined
		m.mu.Unlock()
		return joined
	}

	return nil
}

func (m *Manager) dispatchBackend(ctx context.Context, op domain.Operation, req app.MutationRequest) (domain.Receipt, error) {
	if m.recoveryService == nil {
		return domain.Receipt{}, app.ErrMissingBackend
	}

	switch op.Kind {
	case "machine.start":
		rcpt, _, err := m.recoveryService.StartMachine(ctx, req)
		return rcpt, err
	case "machine.stop":
		mode := "shutdown"
		if m, ok := op.Parameters["mode"].(string); ok && m != "" {
			mode = m
		}
		rcpt, _, err := m.recoveryService.StopMachine(ctx, req, mode)
		return rcpt, err
	case "checkpoint.create":
		name := "checkpoint"
		if n, ok := op.Parameters["name"].(string); ok && n != "" {
			name = n
		}
		rcpt, _, err := m.recoveryService.CreateCheckpoint(ctx, req, name)
		return rcpt, err
	case "checkpoint.restore":
		chkID := ""
		if c, ok := op.Parameters["checkpoint_id"].(string); ok {
			chkID = c
		}
		rcpt, _, err := m.recoveryService.RestoreCheckpoint(ctx, req, chkID)
		return rcpt, err
	default:
		return domain.Receipt{}, domain.ErrInvalidOperationKind
	}
}

func sanitizeExecError(err error) (string, string) {
	var deniedErr *app.PolicyDeniedError
	if errors.As(err, &deniedErr) {
		return string(deniedErr.Reason), deniedErr.Message
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, domain.ErrMissingDeadline) {
		return "timeout", "operation deadline exceeded"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled", "operation cancelled"
	}
	return "backend_error", "backend operation failed"
}
