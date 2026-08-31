package app

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func (s *BootstrapService) finalize(ctx context.Context, op domain.Operation, startedAt time.Time, result BootstrapResult, effectErr error) (*domain.Receipt, error) {
	receiptID, err := domain.GenerateReceiptID()
	if err != nil {
		return nil, err
	}
	fingerprint, err := op.Fingerprint()
	if err != nil {
		return nil, err
	}
	idempotencyFingerprint, err := domain.ComputeIdempotencyFingerprint(op)
	if err != nil {
		return nil, err
	}
	record := domain.Receipt{
		ReceiptID: receiptID, OperationKind: op.Kind, Fingerprint: fingerprint,
		IdempotencyFingerprint: idempotencyFingerprint, IdempotencyKey: op.IdempotencyKey,
		Actor: op.Actor.EffectiveActor, Target: op.Target, Class: op.Classification,
		EffectiveBackend: "windows-task-scheduler", StartedAt: startedAt, CompletedAt: s.now().UTC(),
		Outcome: bootstrapReceiptOutcome(ctx, effectErr), ObservationType: bootstrapReceiptObservation(result),
		EvidenceRefs: bootstrapStateEvidence(result), RedactionStatus: domain.RedactionApplied,
	}
	if err := s.receiptStore.EnsureContext(ctx, record); err != nil {
		return &record, err
	}
	if err := s.auditStore.EnsureTerminalOutcomeContext(ctx, record); err != nil {
		return &record, err
	}
	return &record, nil
}

func bootstrapReceiptOutcome(ctx context.Context, effectErr error) domain.ExecutionOutcome {
	if effectErr == nil {
		return domain.ExecutionOutcome{Status: domain.OutcomeSuccess, ExitCode: 0}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(effectErr, context.DeadlineExceeded) {
		return domain.ExecutionOutcome{
			Status: domain.OutcomeAborted, ExitCode: 1,
			ErrorCategory: "deadline_exceeded", ErrorMessage: "operation deadline exceeded",
		}
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(effectErr, context.Canceled) {
		return domain.ExecutionOutcome{
			Status: domain.OutcomeAborted, ExitCode: 1,
			ErrorCategory: "caller_canceled", ErrorMessage: "operation canceled by caller",
		}
	}
	return domain.ExecutionOutcome{Status: domain.OutcomeFailed, ExitCode: 1}
}

func bootstrapReceiptObservation(result BootstrapResult) domain.ObservationType {
	if result.SchemaVersion == 0 {
		return domain.ObservationInferred
	}
	return domain.ObservationObserved
}

func bootstrapStateEvidence(result BootstrapResult) []string {
	if result.SchemaVersion == 0 {
		return []string{"bootstrap-state-unverified"}
	}
	evidence := []string{"bootstrap-state-" + string(result.Status)}
	if result.TaskStopApplied {
		evidence = append(evidence, "bootstrap-task-stop-applied")
	}
	if result.TaskRunning && result.Status != BootstrapHealthy {
		evidence = append(evidence, "bootstrap-task-still-running")
	}
	return evidence
}

func bootstrapReceiptContainsEvidence(record domain.Receipt, want string) bool {
	return slices.Contains(record.EvidenceRefs, want)
}
