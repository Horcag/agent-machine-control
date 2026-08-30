package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
)

func classifyRunError(runErr error, effectOccurred bool) (domain.OutcomeStatus, int, string, string) {
	var deniedErr *PolicyDeniedError
	switch {
	case errors.As(runErr, &deniedErr):
		return domain.OutcomeDenied, 7, string(deniedErr.Reason), deniedErr.Message
	case effectOccurred:
		return domain.OutcomeFailed, 1, "", ""
	case errors.Is(runErr, context.DeadlineExceeded) || errors.Is(runErr, domain.ErrMissingDeadline):
		return domain.OutcomeAborted, 1, "", ""
	case errors.Is(runErr, context.Canceled):
		return domain.OutcomeAborted, 1, "", ""
	default:
		return domain.OutcomeFailed, 1, "", ""
	}
}

func (s *SessionService) persistOutcome(
	op domain.Operation,
	fp, idFp domain.Fingerprint,
	_ policy.Decision,
	startedAt, completedAt time.Time,
	runErr error,
	rollbackRef string,
	evidenceRefs []string,
	exitCode int,
	effectOccurred bool,
) (domain.Receipt, error) {
	rcpt, err := s.buildOutcomeReceipt(op, fp, idFp, startedAt, completedAt, runErr, rollbackRef, evidenceRefs, exitCode, effectOccurred)
	if err != nil {
		return domain.Receipt{}, err
	}
	return rcpt, s.persistTerminalOutcome(rcpt)
}

func (s *SessionService) buildOutcomeReceipt(
	op domain.Operation,
	fp, idFp domain.Fingerprint,
	startedAt, completedAt time.Time,
	runErr error,
	rollbackRef string,
	evidenceRefs []string,
	exitCode int,
	effectOccurred bool,
) (domain.Receipt, error) {
	outcomeStatus := domain.OutcomeSuccess
	effectiveRollback := rollbackRef
	var errCategory, errMsg string
	if runErr != nil {
		if !effectOccurred {
			effectiveRollback = ""
		}
		outcomeStatus, exitCode, errCategory, errMsg = classifyRunError(runErr, effectOccurred)
	}
	rcptID, err := domain.GenerateReceiptID()
	if err != nil {
		return domain.Receipt{}, fmt.Errorf("app: failed to generate receipt ID: %w", err)
	}
	return domain.Receipt{
		ReceiptID: rcptID, OperationKind: op.Kind, Fingerprint: fp, IdempotencyFingerprint: idFp,
		IdempotencyKey: op.IdempotencyKey, Actor: op.Actor.EffectiveActor, Target: op.Target, Class: op.Classification,
		EffectiveBackend: "amcd", StartedAt: startedAt, CompletedAt: completedAt,
		Outcome: domain.ExecutionOutcome{
			Status: outcomeStatus, ExitCode: exitCode, ErrorCategory: errCategory, ErrorMessage: errMsg,
		},
		ObservationType: domain.ObservationObserved, RollbackRef: effectiveRollback,
		RedactionStatus: domain.RedactionApplied, EvidenceRefs: evidenceRefs,
	}, nil
}

func (s *SessionService) persistTerminalOutcome(rcpt domain.Receipt) error {
	return s.persistTerminalOutcomeContext(context.Background(), rcpt)
}

func (s *SessionService) persistTerminalOutcomeContext(ctx context.Context, rcpt domain.Receipt) error {
	if s.receiptStore == nil {
		return errors.New("receipt store: unavailable")
	}
	if err := s.receiptStore.EnsureContext(ctx, rcpt); err != nil {
		return fmt.Errorf("receipt store: %w", err)
	}
	if s.auditStore == nil {
		return errors.New("audit store: unavailable")
	}
	if err := s.auditStore.EnsureTerminalOutcomeContext(ctx, rcpt); err != nil {
		return fmt.Errorf("audit store: %w", err)
	}
	return nil
}
