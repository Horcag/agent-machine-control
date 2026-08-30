package operations

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/events"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

// ReconcileCrashedOperations scans for non-terminal operations from a previous daemon run,
// transitions them to cancelled/aborted with category daemon_crash_recovered, and writes synthetic receipts.
func ReconcileCrashedOperations(
	ctx context.Context,
	dir string,
	receiptStore *receipt.Store,
	auditStore *audit.Store,
	eventHub *events.Hub,
	now time.Time,
) ([]string, error) {
	records, err := ListRecordsContext(ctx, dir, ListOptions{Limit: 1000})
	if err != nil {
		return nil, err
	}

	var reconciled []string
	for _, rec := range records {
		if err := ctx.Err(); err != nil {
			return reconciled, err
		}
		if rec.State.IsTerminal() {
			continue
		}

		// Non-terminal operation from dead daemon
		rec.State = domain.OpStateCancelled
		rec.ErrorCategory = "daemon_crash_recovered"
		rec.ErrorMessage = "daemon terminated unexpectedly during execution"
		rec.CompletedAt = now

		rcptID, err := ensureSyntheticReceiptContext(ctx, &rec, receiptStore, auditStore, now)
		if err != nil {
			return reconciled, fmt.Errorf("operations: failed to create synthetic receipt during crash recovery: %w", err)
		}

		if err := SaveRecordContext(ctx, dir, rec); err != nil {
			return reconciled, fmt.Errorf("operations: failed to persist reconciled record %s: %w", rec.ID, err)
		}

		if eventHub != nil {
			if _, err := eventHub.Publish(ctx, events.Event{
				OperationID: rec.ID,
				Target:      rec.Target,
				EventType:   "terminal",
				State:       domain.OpStateCancelled,
				Category:    "daemon_crash_recovered",
				Message:     "daemon terminated unexpectedly during execution",
				ReceiptID:   domain.ReceiptID(rcptID),
			}); err != nil {
				return reconciled, fmt.Errorf("operations: failed to publish terminal event for reconciled record %s: %w", rec.ID, err)
			}
		}

		reconciled = append(reconciled, rec.ID)
	}

	return reconciled, nil
}

func ensureSyntheticReceipt(
	rec *domain.OperationRecord,
	receiptStore *receipt.Store,
	auditStore *audit.Store,
	now time.Time,
) (string, error) {
	return ensureSyntheticReceiptContext(context.Background(), rec, receiptStore, auditStore, now)
}

func ensureSyntheticReceiptContext(
	ctx context.Context,
	rec *domain.OperationRecord,
	receiptStore *receipt.Store,
	auditStore *audit.Store,
	now time.Time,
) (string, error) {
	if rec.ReceiptID != "" {
		return string(rec.ReceiptID), nil
	}
	if receiptStore == nil {
		return "", nil
	}
	generated, err := generateReceiptID()
	if err != nil {
		return "", err
	}
	rcpt := domain.Receipt{
		ReceiptID:              domain.ReceiptID(generated),
		OperationKind:          rec.Kind,
		Fingerprint:            rec.Fingerprint,
		IdempotencyFingerprint: rec.IdempotencyFingerprint,
		IdempotencyKey:         rec.IdempotencyKey,
		Actor:                  rec.Actor,
		Target:                 rec.Target,
		Class:                  rec.EffectiveClass,
		EffectiveBackend:       "hyperv",
		StartedAt:              rec.CreatedAt,
		CompletedAt:            now,
		Outcome: domain.ExecutionOutcome{
			Status:   domain.OutcomeAborted,
			ExitCode: 1,
		},
		ObservationType: domain.ObservationInferred,
		RedactionStatus: domain.RedactionApplied,
	}
	if err := receiptStore.EnsureContext(ctx, rcpt); err != nil {
		return "", fmt.Errorf("failed to save synthetic receipt: %w", err)
	}
	rec.ReceiptID = domain.ReceiptID(generated)
	if auditStore != nil {
		if err := auditStore.EnsureTerminalOutcomeContext(ctx, rcpt); err != nil {
			return generated, fmt.Errorf("failed to record synthetic audit outcome: %w", err)
		}
	}
	return generated, nil
}

func generateReceiptID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto/rand error: %w", err)
	}
	return fmt.Sprintf("rcpt-%s", hex.EncodeToString(b)), nil
}
