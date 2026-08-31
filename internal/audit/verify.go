package audit

import (
	"context"
	"fmt"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

// VerifyTerminalOutcome proves that exactly one durable terminal audit event matches the receipt.
func (s *Store) VerifyTerminalOutcome(receipt domain.Receipt) error {
	return s.VerifyTerminalOutcomeContext(context.Background(), receipt)
}

// VerifyTerminalOutcomeContext verifies terminal evidence within the caller's deadline.
func (s *Store) VerifyTerminalOutcomeContext(ctx context.Context, receipt domain.Receipt) error {
	if s == nil {
		return fmt.Errorf("%w: audit store is unavailable", ErrTerminalEvidenceInvalid)
	}

	if err := lockAuditStoreContext(ctx, &s.mu); err != nil {
		return err
	}
	defer s.mu.Unlock()
	events, err := s.readEventsLockedContext(ctx)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTerminalEvidenceInvalid, err)
	}
	var matched bool
	for _, event := range events {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !validAuditEnvelope(event) {
			return fmt.Errorf("%w: invalid audit event envelope", ErrTerminalEvidenceInvalid)
		}
		if event.EventType != EventTerminalOutcome || event.ReceiptID != string(receipt.ReceiptID) {
			continue
		}
		if matched {
			return fmt.Errorf("%w: duplicate terminal receipt evidence", ErrTerminalEvidenceInvalid)
		}
		matched = true
		if !terminalIdentityMatches(event, receipt) || !terminalOutcomeMatches(event, receipt) {
			return fmt.Errorf("%w: terminal audit event does not match receipt", ErrTerminalEvidenceInvalid)
		}
	}
	if !matched {
		return fmt.Errorf("%w: terminal receipt evidence not found", ErrTerminalEvidenceInvalid)
	}
	return nil
}

func validAuditEnvelope(event Event) bool {
	return event.SchemaVersion == SchemaVersion && (event.EventType == EventAdmissionIntent || event.EventType == EventTerminalOutcome)
}

func terminalIdentityMatches(event Event, receipt domain.Receipt) bool {
	return event.Actor == string(receipt.Actor) && event.Target == string(receipt.Target) &&
		event.OperationKind == string(receipt.OperationKind) && event.Fingerprint == string(receipt.Fingerprint) &&
		event.IdempotencyFingerprint == string(receipt.IdempotencyFingerprint) && event.IdempotencyKey == receipt.IdempotencyKey &&
		event.Classification == receipt.Class
}

func terminalOutcomeMatches(event Event, receipt domain.Receipt) bool {
	return event.OutcomeStatus == receipt.Outcome.Status && event.ExitCode == receipt.Outcome.ExitCode &&
		event.ErrorCategory == receipt.Outcome.ErrorCategory && event.ErrorMessage == receipt.Outcome.ErrorMessage &&
		event.RollbackRef == receipt.RollbackRef
}
