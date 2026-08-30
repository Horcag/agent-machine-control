package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
	"github.com/Horcag/agent-machine-control/internal/sessions"
)

func (s *SessionService) evaluatePolicy(op domain.Operation, rollbackState policy.RollbackState, approval *domain.Approval, now time.Time) (policy.Decision, error) {
	auditWritable := s.auditStore != nil && s.auditStore.CheckWritable() == nil &&
		s.receiptStore != nil && s.receiptStore.CheckWritable() == nil &&
		s.mutationJournal != nil && s.mutationJournal.CheckWritable() == nil

	evalInput := policy.EvaluationInput{
		Operation:               op,
		Now:                     now,
		AuditWritable:           auditWritable,
		RollbackState:           rollbackState,
		Approval:                approval,
		RollbackPolicy:          policy.RollbackPolicyEscalateToDestructive,
		AvailableCapabilities:   domain.SessionCapabilities(),
		SensitiveEvidenceScopes: domain.NewScopeSet("evidence:sensitive", policy.DefaultSensitiveEvidenceScope),
	}

	decision := policy.Evaluate(evalInput)
	if decision.Type == policy.DecisionDeny {
		return decision, &PolicyDeniedError{
			Reason:  decision.DenialReason,
			Message: decision.DenialMessage,
		}
	}
	return decision, nil
}

func (s *SessionService) acquireFlight(flightKey string, idFp domain.Fingerprint) (*inFlightSessionCall, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if inFlight, exists := s.inFlight[flightKey]; exists {
		if inFlight.idFp != idFp {
			return nil, false, fmt.Errorf("%w: concurrent session mutation conflict", domain.ErrSessionConflict)
		}
		return inFlight, true, nil
	}
	entry := &inFlightSessionCall{done: make(chan struct{}), idFp: idFp}
	s.inFlight[flightKey] = entry
	return entry, false, nil
}

func (s *SessionService) releaseFlight(flightKey string, entry *inFlightSessionCall) {
	s.mu.Lock()
	delete(s.inFlight, flightKey)
	close(entry.done)
	s.mu.Unlock()
}

func (s *SessionService) validateAndFingerprint(op domain.Operation) (domain.Fingerprint, domain.Fingerprint, error) {
	if err := op.Validate(); err != nil {
		return "", "", err
	}
	if err := domain.ValidateOperationParameters(op.Kind, op.Parameters); err != nil {
		return "", "", err
	}
	fp, err := op.Fingerprint()
	if err != nil {
		return "", "", err
	}
	idFp, err := domain.ComputeIdempotencyFingerprint(op)
	if err != nil {
		return "", "", err
	}
	return fp, idFp, nil
}

func (s *SessionService) acquireMutationLease(
	ctx context.Context,
	op domain.Operation,
	timeout time.Duration,
	fp domain.Fingerprint,
) (func() error, error) {
	if s.leaseMgr == nil {
		return func() error { return nil }, nil
	}
	leaseTTL := timeout + 15*time.Second
	l, err := s.leaseMgr.Acquire(ctx, string(op.Target), string(op.Kind), string(fp), leaseTTL)
	if err != nil {
		return nil, err
	}
	return func() error {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return s.leaseMgr.Release(cleanupCtx, l)
	}, nil
}

func (s *SessionService) resolveSafety(ctx context.Context, target domain.MachineRef) SafetyResolution {
	if s.safetyResolver == nil {
		return SafetyResolution{Classification: domain.ClassDestructivePrivileged}
	}
	sr, err := s.safetyResolver.ResolveSafety(ctx, target)
	if err != nil {
		return SafetyResolution{Classification: domain.ClassDestructivePrivileged}
	}
	return sr
}

func (s *SessionService) checkDestructiveApproval(approval *domain.Approval) error {
	if approval == nil {
		return &PolicyDeniedError{
			Reason:  policy.DenialApprovalRequired,
			Message: "destructive/privileged operation requires operator approval",
		}
	}
	if s.approvalStore == nil {
		return errors.New("app: approval store is unavailable")
	}
	if err := s.approvalStore.CheckWritable(); err != nil {
		return fmt.Errorf("app: approval store is unwritable: %w", err)
	}
	consumed, err := s.approvalStore.IsConsumed(string(approval.ID))
	if err != nil {
		return err
	}
	if consumed {
		return domain.ErrApprovalConsumed
	}
	return nil
}

func (s *SessionService) prepareAuditAndApproval(decision policy.Decision, op domain.Operation, approval *domain.Approval, now time.Time) error {
	if s.auditStore != nil {
		if err := s.auditStore.RecordAdmissionIntent(op); err != nil {
			return err
		}
	}
	if decision.EffectiveClass.RequiresApproval() && approval != nil && s.approvalStore != nil {
		if err := s.approvalStore.MarkConsumed(*approval, now); err != nil {
			return fmt.Errorf("app: failed to record approval consumption: %w", err)
		}
	}
	return nil
}

func extractEvidenceRefs(op domain.Operation, obs *domain.SessionObservation) []string {
	if op.Kind == "session.open" && obs != nil {
		return []string{string(obs.ID)}
	}
	if op.Kind == "session.close" {
		if sID, ok := op.Parameters["session_id"].(string); ok {
			return []string{sID}
		}
	}
	if sID, ok := op.Parameters["session_id"].(string); ok {
		return []string{sID}
	}
	return nil
}

func waitForInFlight(ctx context.Context, entry *inFlightSessionCall) (sessionMutationResult, *domain.Receipt, error) {
	select {
	case <-entry.done:
		if !entry.hasReceipt {
			return entry.result, nil, entry.err
		}
		return entry.result, &entry.rcpt, entry.err
	case <-ctx.Done():
		return sessionMutationResult{}, nil, ctx.Err()
	}
}

func (s *SessionService) evaluateAndAdmit(
	op domain.Operation,
	safetyRes SafetyResolution,
	approval *domain.Approval,
	now time.Time,
	fp, idFp domain.Fingerprint,
) (policy.Decision, *domain.Receipt, error) {
	decision, pErr := s.evaluatePolicy(op, safetyRes.RollbackState, approval, now)
	if pErr != nil {
		rcpt, persistErr := s.persistOutcome(op, fp, idFp, decision, now, now, pErr, "", nil, 7, false)
		return decision, &rcpt, errors.Join(pErr, persistErr)
	}

	if decision.EffectiveClass.RequiresApproval() {
		if aErr := s.checkDestructiveApproval(approval); aErr != nil {
			rcpt, persistErr := s.persistOutcome(op, fp, idFp, decision, now, now, aErr, "", nil, 7, false)
			return decision, &rcpt, errors.Join(aErr, persistErr)
		}
	}
	return decision, nil, nil
}

func (s *SessionService) executeMutation(
	execCtx context.Context,
	op domain.Operation,
	fp, idFp domain.Fingerprint,
	safetyRes SafetyResolution,
	execute func(context.Context) (sessionMutationResult, error),
) (sessionMutationResult, domain.Receipt, error) {
	startedAt := s.now()
	var result sessionMutationResult
	runErr := execCtx.Err()
	if runErr == nil {
		result, runErr = execute(execCtx)
	}
	completedAt := s.now()
	if !completedAt.After(startedAt) {
		completedAt = startedAt.Add(time.Millisecond)
	}

	rollbackRef := ""
	if result.EffectApplied && op.Classification == domain.ClassReversibleMutation {
		rollbackRef = safetyRes.RollbackRef
	}

	var evidenceRefs []string
	if result.EffectApplied {
		evidenceRefs = result.EvidenceRefs
		if len(evidenceRefs) == 0 {
			evidenceRefs = extractEvidenceRefs(op, result.Observation)
		}
	}
	rcpt, persistErr := s.buildOutcomeReceipt(op, fp, idFp, startedAt, completedAt, runErr, rollbackRef, evidenceRefs, result.ExitCode, result.EffectApplied)
	effectApplied := result.EffectApplied
	durableResult := sessions.MutationResult{
		BytesWritten:  result.BytesWritten,
		Observation:   result.Observation,
		EffectApplied: &effectApplied,
	}
	if persistErr == nil && s.mutationJournal == nil {
		persistErr = errors.New("app: session mutation journal is unavailable")
	}
	finalizationCtx, cancelFinalization := context.WithTimeout(context.WithoutCancel(execCtx), 5*time.Second)
	defer cancelFinalization()
	if persistErr == nil {
		persistErr = s.mutationJournal.RecordFinalizationIntentContext(finalizationCtx, op, rcpt, durableResult, completedAt)
	}
	if persistErr == nil {
		persistErr = s.persistTerminalOutcome(rcpt)
	}
	if persistErr == nil {
		persistErr = s.mutationJournal.MarkFinalizedContext(finalizationCtx, op, completedAt)
	}
	if runErr != nil {
		return result, rcpt, errors.Join(runErr, persistErr)
	}
	if persistErr != nil {
		return result, rcpt, fmt.Errorf("app: session mutation succeeded but durable finalization failed: %w", persistErr)
	}
	return result, rcpt, nil
}

type admittedSessionMutation struct {
	op           domain.Operation
	fp           domain.Fingerprint
	idFp         domain.Fingerprint
	decision     policy.Decision
	safety       SafetyResolution
	releaseLease func() error
}

func (s *SessionService) admitSessionMutation(
	ctx context.Context,
	op domain.Operation,
	approval *domain.Approval,
	timeout time.Duration,
	initialFP domain.Fingerprint,
) (*admittedSessionMutation, *domain.Receipt, error) {
	releaseLease, err := s.acquireMutationLease(ctx, op, timeout, initialFP)
	if err != nil {
		return nil, nil, err
	}
	now := s.now()
	safety := s.resolveSafety(ctx, op.Target)
	op.Classification = safety.Classification
	fp, idFp, err := s.validateAndFingerprint(op)
	if err != nil {
		_ = releaseLease()
		return nil, nil, err
	}
	if ctx.Err() != nil {
		return &admittedSessionMutation{
			op: op, fp: fp, idFp: idFp,
			decision:     policy.Decision{Type: policy.DecisionAllow, EffectiveClass: op.Classification},
			safety:       safety,
			releaseLease: releaseLease,
		}, nil, nil
	}
	decision, denialReceipt, err := s.evaluateAndAdmit(op, safety, approval, now, fp, idFp)
	if err != nil {
		_ = releaseLease()
		return nil, denialReceipt, err
	}
	return &admittedSessionMutation{
		op: op, fp: fp, idFp: idFp, decision: decision, safety: safety, releaseLease: releaseLease,
	}, nil, nil
}

func (s *SessionService) reserveAndPrepareMutation(ctx context.Context, admitted *admittedSessionMutation, approval *domain.Approval) error {
	if s.mutationJournal == nil {
		_ = admitted.releaseLease()
		return errors.New("app: session mutation journal is unavailable")
	}
	now := s.now()
	if _, err := s.mutationJournal.Reserve(admitted.op, now); err != nil {
		_ = admitted.releaseLease()
		return err
	}
	if err := s.prepareAuditAndApproval(admitted.decision, admitted.op, approval, now); err != nil {
		cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		cancelErr := s.mutationJournal.CancelContext(cancelCtx, admitted.op)
		cancel()
		_ = admitted.releaseLease()
		return errors.Join(err, cancelErr)
	}
	return nil
}

func (s *SessionService) coordinateSessionMutation(
	ctx context.Context,
	op domain.Operation,
	flightKey string,
	approval *domain.Approval,
	timeout time.Duration,
	execute func(context.Context) (sessionMutationResult, error),
) (sessionMutationResult, *domain.Receipt, error) {
	initialFP, idFp, err := s.validateAndFingerprint(op)
	if err != nil {
		return sessionMutationResult{}, nil, err
	}

	entry, isWaiting, err := s.acquireFlight(flightKey, idFp)
	if err != nil {
		return sessionMutationResult{}, nil, err
	}
	if isWaiting {
		return waitForInFlight(ctx, entry)
	}
	defer s.releaseFlight(flightKey, entry)

	if n, obs, rcpt, handled, retryErr := s.lookupSessionRetry(ctx, op); handled {
		entry.result = sessionMutationResult{BytesWritten: n, Observation: obs}
		if rcpt != nil {
			entry.rcpt = *rcpt
			entry.hasReceipt = true
		}
		entry.err = retryErr
		return sessionMutationResult{BytesWritten: n, Observation: obs}, rcpt, retryErr
	}
	if err := ctx.Err(); err != nil {
		entry.err = err
		return sessionMutationResult{}, nil, err
	}
	if approval != nil && !op.Actor.HasScope(domain.ScopeSessionAdmin) {
		now := s.now()
		denial := &PolicyDeniedError{
			Reason:  policy.DenialApprovalRequired,
			Message: "raw approval objects require an authenticated session administrator",
		}
		decision := policy.Decision{Type: policy.DecisionDeny, EffectiveClass: op.Classification}
		rcpt, persistErr := s.persistOutcome(op, initialFP, idFp, decision, now, now, denial, "", nil, 7, false)
		finalErr := errors.Join(denial, persistErr)
		entry.rcpt = rcpt
		entry.hasReceipt = true
		entry.err = finalErr
		return sessionMutationResult{}, &rcpt, finalErr
	}

	admitted, denialRcpt, err := s.admitSessionMutation(ctx, op, approval, timeout, initialFP)
	if err != nil {
		entry.err = err
		if denialRcpt != nil {
			entry.rcpt = *denialRcpt
			entry.hasReceipt = true
		}
		return sessionMutationResult{}, denialRcpt, err
	}
	if err := s.reserveAndPrepareMutation(ctx, admitted, approval); err != nil {
		entry.err = err
		return sessionMutationResult{}, nil, err
	}

	result, rcpt, mutErr := s.executeMutation(ctx, admitted.op, admitted.fp, admitted.idFp, admitted.safety, execute)
	releaseErr := admitted.releaseLease()

	entry.result = result
	entry.rcpt = rcpt
	entry.hasReceipt = true

	if mutErr != nil || releaseErr != nil {
		finalErr := errors.Join(mutErr, releaseErr)
		entry.err = finalErr
		return result, &rcpt, finalErr
	}

	return result, &rcpt, nil
}
