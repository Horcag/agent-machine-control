package operations

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func (m *Manager) checkCachedReceipt(op domain.Operation) (*domain.OperationRecord, bool, error) {
	if m.receiptStore == nil {
		return nil, false, nil
	}
	cachedRcpt, rcptErr := m.receiptStore.LookupIdempotency(op)
	if rcptErr != nil {
		if errors.Is(rcptErr, receipt.ErrIdempotencyCollision) {
			return nil, false, ErrOperationConflict
		}
		return nil, false, rcptErr
	}
	if cachedRcpt == nil {
		return nil, false, nil
	}
	idFp, err := domain.ComputeIdempotencyFingerprint(op)
	if err != nil {
		return nil, false, fmt.Errorf("operations: failed to compute idempotency fingerprint: %w", err)
	}

	if err := verifyCachedReceiptMatch(op, cachedRcpt, idFp); err != nil {
		return nil, false, err
	}

	state, errorCategory, errorMessage := mapOutcome(cachedRcpt.Outcome)

	if err := domain.ValidateReceiptID(string(cachedRcpt.ReceiptID)); err != nil {
		return nil, false, fmt.Errorf("operations: invalid cached receipt ID: %w", err)
	}

	h := sha256.New()
	h.Write([]byte("AMC_CACHED_OP_V1\x00"))
	h.Write([]byte(cachedRcpt.ReceiptID))
	sum := h.Sum(nil)
	opID := "op-" + hex.EncodeToString(sum[:16])

	if err := domain.ValidateOperationID(opID); err != nil {
		return nil, false, fmt.Errorf("operations: derived operation ID is invalid: %w", err)
	}

	rec := &domain.OperationRecord{
		SchemaVersion:          "1",
		ID:                     opID,
		Actor:                  cachedRcpt.Actor,
		Target:                 cachedRcpt.Target,
		Kind:                   cachedRcpt.OperationKind,
		RequestedClass:         cachedRcpt.Class,
		EffectiveClass:         cachedRcpt.Class,
		Fingerprint:            cachedRcpt.Fingerprint,
		IdempotencyFingerprint: cachedRcpt.IdempotencyFingerprint,
		IdempotencyKey:         cachedRcpt.IdempotencyKey,
		Deadline:               cachedRcpt.CompletedAt,
		State:                  state,
		CreatedAt:              cachedRcpt.StartedAt,
		CompletedAt:            cachedRcpt.CompletedAt,
		ReceiptID:              cachedRcpt.ReceiptID,
		Parameters:             op.Parameters,
		ErrorCategory:          errorCategory,
		ErrorMessage:           errorMessage,
	}

	return m.saveReconstructedRecord(rec)
}

func (m *Manager) saveReconstructedRecord(rec *domain.OperationRecord) (*domain.OperationRecord, bool, error) {
	if m.dir == "" {
		return nil, false, ErrPersistenceFailure
	}
	existing, err := ReadRecord(m.dir, rec.ID)
	if err == nil {
		if !isCompatible(existing, rec) {
			return nil, false, ErrOperationConflict
		}
		return existing, true, nil
	}
	if !errors.Is(err, ErrOperationNotFound) {
		return nil, false, err
	}

	if err := SaveRecord(m.dir, *rec); err != nil {
		return nil, false, fmt.Errorf("operations: failed to persist cached operation: %w", err)
	}

	return rec, true, nil
}

func isCompatible(existing, rec *domain.OperationRecord) bool {
	return existing.ReceiptID == rec.ReceiptID &&
		existing.Fingerprint == rec.Fingerprint &&
		existing.IdempotencyFingerprint == rec.IdempotencyFingerprint &&
		existing.IdempotencyKey == rec.IdempotencyKey &&
		existing.Actor == rec.Actor &&
		existing.Target == rec.Target &&
		existing.Kind == rec.Kind &&
		existing.RequestedClass == rec.RequestedClass &&
		existing.EffectiveClass == rec.EffectiveClass &&
		existing.State == rec.State &&
		existing.ErrorCategory == rec.ErrorCategory &&
		existing.ErrorMessage == rec.ErrorMessage &&
		existing.CreatedAt.Equal(rec.CreatedAt) &&
		existing.CompletedAt.Equal(rec.CompletedAt) &&
		existing.Deadline.Equal(rec.Deadline)
}

func verifyCachedReceiptMatch(op domain.Operation, cachedRcpt *domain.Receipt, idFp domain.Fingerprint) error {
	if cachedRcpt.Actor != op.Actor.EffectiveActor || cachedRcpt.Target != op.Target {
		return ErrOperationConflict
	}
	actualIDFP := cachedRcpt.IdempotencyFingerprint
	var expectedIDFP domain.Fingerprint
	if actualIDFP == "" {
		actualIDFP = cachedRcpt.Fingerprint
		fp, err := op.Fingerprint()
		if err != nil {
			return err
		}
		expectedIDFP = fp
	} else {
		expectedIDFP = idFp
	}
	if actualIDFP != expectedIDFP {
		return ErrOperationConflict
	}
	return nil
}

func mapOutcome(outcome domain.ExecutionOutcome) (domain.OperationState, string, string) {
	switch outcome.Status {
	case domain.OutcomeSuccess:
		return domain.OpStateCompleted, "", ""
	case domain.OutcomeDenied:
		return domain.OpStateFailed, outcome.ErrorCategory, outcome.ErrorMessage
	case domain.OutcomeFailed:
		return domain.OpStateFailed, "", ""
	case domain.OutcomeAborted:
		return domain.OpStateCancelled, "", ""
	default:
		return domain.OpStatePending, "", ""
	}
}
