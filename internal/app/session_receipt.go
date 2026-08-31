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
		if category, message, ok := canonicalSessionFailure(runErr); ok {
			return domain.OutcomeFailed, 1, category, message
		}
		return domain.OutcomeFailed, 1, "", ""
	case errors.Is(runErr, context.DeadlineExceeded):
		message, _ := domain.CanonicalFailureMessage(domain.FailureCategoryDeadlineExceeded)
		return domain.OutcomeAborted, 1, domain.FailureCategoryDeadlineExceeded, message
	case errors.Is(runErr, context.Canceled):
		message, _ := domain.CanonicalFailureMessage(domain.FailureCategoryCallerCanceled)
		return domain.OutcomeAborted, 1, domain.FailureCategoryCallerCanceled, message
	default:
		if category, message, ok := canonicalSessionFailure(runErr); ok {
			return domain.OutcomeFailed, 1, category, message
		}
		return domain.OutcomeFailed, 1, "", ""
	}
}

func canonicalSessionFailure(runErr error) (string, string, bool) {
	for _, failure := range canonicalSessionFailures {
		if errors.Is(runErr, failure.err) {
			message, _ := domain.CanonicalFailureMessage(failure.category)
			return failure.category, message, true
		}
	}
	return "", "", false
}

var canonicalSessionFailures = []struct {
	category string
	err      error
}{
	{domain.FailureCategoryCallerCanceled, context.Canceled},
	{domain.FailureCategoryDeadlineExceeded, context.DeadlineExceeded},
	{domain.FailureCategorySessionNotFound, domain.ErrSessionNotFound},
	{domain.FailureCategorySessionAccessDenied, domain.ErrSessionAccessDenied},
	{domain.FailureCategorySessionClosed, domain.ErrSessionClosed},
	{domain.FailureCategorySessionConflict, domain.ErrSessionConflict},
	{domain.FailureCategorySessionWaitTimeout, domain.ErrSessionWaitTimeout},
	{domain.FailureCategoryHostKeyMismatch, domain.ErrHostKeyMismatch},
	{domain.FailureCategoryMissingHostKeyPin, domain.ErrMissingHostKeyPin},
	{domain.FailureCategoryNonCanonicalParameter, domain.ErrNonCanonicalParameter},
	{domain.FailureCategoryInvalidControlKey, domain.ErrInvalidControlKey},
	{domain.FailureCategoryInvalidTerminalDimensions, domain.ErrInvalidTerminalDimensions},
	{domain.FailureCategoryInvalidTerminalType, domain.ErrInvalidTerminalType},
	{domain.FailureCategoryInvalidApprovalRecord, domain.ErrInvalidApprovalRecord},
	{domain.FailureCategoryMissingDeadline, domain.ErrMissingDeadline},
}

func (s *SessionService) persistOutcome(
	ctx context.Context,
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
	return rcpt, s.persistTerminalOutcomeContext(ctx, rcpt)
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
