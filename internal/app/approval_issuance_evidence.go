package app

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func buildApprovalIssuanceReceipt(namespace string, op domain.Operation, issued domain.Approval) (domain.Receipt, error) {
	fingerprint, err := op.Fingerprint()
	if err != nil {
		return domain.Receipt{}, err
	}
	idempotencyFingerprint, err := domain.ComputeIdempotencyFingerprint(op)
	if err != nil {
		return domain.Receipt{}, err
	}
	digest := sha256.Sum256([]byte(namespace + "\x00" + string(issued.ID)))
	receipt := domain.Receipt{
		ReceiptID:     domain.ReceiptID("rcpt-" + hex.EncodeToString(digest[:16])),
		OperationKind: op.Kind, Fingerprint: fingerprint, IdempotencyFingerprint: idempotencyFingerprint,
		IdempotencyKey: op.IdempotencyKey, Actor: op.Actor.EffectiveActor, Target: op.Target,
		Class: op.Classification, EffectiveBackend: "amcd", StartedAt: issued.IssuedAt, CompletedAt: issued.IssuedAt,
		Outcome:         domain.ExecutionOutcome{Status: domain.OutcomeSuccess, ExitCode: 0},
		ObservationType: domain.ObservationObserved, EvidenceRefs: []string{string(issued.ID)},
		RedactionStatus: domain.RedactionApplied,
	}
	return receipt, receipt.Validate()
}
