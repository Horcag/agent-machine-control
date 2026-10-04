package audit

import (
	"context"
	"fmt"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

// TerminalOutcomeRequest identifies canonical terminal evidence to reconcile.
type TerminalOutcomeRequest struct {
	Receipt domain.Receipt
	// RequireExisting prevents reconstruction of legacy evidence without a durable intent.
	RequireExisting bool
}

// EnsureTerminalOutcomesContext validates one locked audit snapshot for all requests.
// Missing canonical events are appended durably; legacy events must already exist.
// A failure may leave a durable prefix of appends, which an exact retry can recover.
func (s *Store) EnsureTerminalOutcomesContext(ctx context.Context, requests []TerminalOutcomeRequest) error {
	if s == nil {
		return ErrAuditUnavailable
	}
	for _, request := range requests {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := request.Receipt.Validate(); err != nil {
			return fmt.Errorf("audit: invalid terminal receipt: %w", err)
		}
	}
	if len(requests) == 0 {
		return ctx.Err()
	}
	if err := lockAuditStoreContext(ctx, &s.mu); err != nil {
		return err
	}
	defer s.mu.Unlock()
	return s.withLockContext(ctx, func() error {
		return s.ensureTerminalOutcomesLocked(ctx, requests)
	})
}

func (s *Store) ensureTerminalOutcomesLocked(ctx context.Context, requests []TerminalOutcomeRequest) error {
	events, err := s.readEventsLockedContext(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrTerminalEvidenceInvalid, err)
	}
	index := make(map[string][]Event)
	for _, event := range events {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !validAuditEnvelope(event) {
			return fmt.Errorf("%w: invalid audit event envelope", ErrTerminalEvidenceInvalid)
		}
		if event.EventType == EventTerminalOutcome {
			index[event.ReceiptID] = append(index[event.ReceiptID], event)
		}
	}
	// Repair directory durability once for evidence found in the snapshot, as
	// single-receipt ensure does after an interrupted close or directory sync.
	if err := s.syncDirectory(); err != nil {
		return fmt.Errorf("%w: failed to sync audit directory: %w", ErrAuditUnavailable, err)
	}
	for _, request := range requests {
		if err := ctx.Err(); err != nil {
			return err
		}
		id := string(request.Receipt.ReceiptID)
		found, err := findExactTerminalOutcome(index[id], request.Receipt)
		if err != nil {
			return err
		}
		if found {
			continue
		}
		if request.RequireExisting {
			return fmt.Errorf("%w: terminal receipt evidence not found", ErrTerminalEvidenceInvalid)
		}
		event := terminalOutcomeEvent(request.Receipt)
		if err := s.appendTerminalOutcomeLocked(ctx, event); err != nil {
			return err
		}
		index[id] = []Event{event}
	}
	return nil
}
