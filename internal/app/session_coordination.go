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

func (s *SessionService) evaluatePolicy(ctx context.Context, op domain.Operation, rollbackState policy.RollbackState, approval *domain.Approval, now time.Time) (policy.Decision, error) {
	auditWritable := s.auditStore != nil && s.auditStore.CheckWritableContext(ctx) == nil &&
		s.receiptStore != nil && s.receiptStore.CheckWritableContext(ctx) == nil &&
		s.mutationJournal != nil && s.mutationJournal.CheckWritableContext(ctx) == nil
	if err := ctx.Err(); err != nil {
		return policy.Decision{}, err
	}

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

// mutationLeaseFingerprint binds immutable request identity while deliberately
// excluding the safety classification that is resolved only after lease ownership.
func mutationLeaseFingerprint(op domain.Operation) (domain.Fingerprint, error) {
	leaseOp := op.Clone()
	leaseOp.Classification = domain.ClassReversibleMutation
	return leaseOp.Fingerprint()
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

func (s *SessionService) checkDestructiveApproval(ctx context.Context, approval *domain.Approval) error {
	if approval == nil {
		return &PolicyDeniedError{
			Reason:  policy.DenialApprovalRequired,
			Message: "destructive/privileged operation requires operator approval",
		}
	}
	if s.approvalStore == nil {
		return errors.New("app: approval store is unavailable")
	}
	if err := s.approvalStore.CheckWritableContext(ctx); err != nil {
		return fmt.Errorf("app: approval store is unwritable: %w", err)
	}
	if err := s.approvalStore.ValidateIssuedContext(ctx, *approval); err != nil {
		return fmt.Errorf("app: approval provenance is invalid: %w", err)
	}
	consumed, err := s.approvalStore.IsConsumedContext(ctx, string(approval.ID))
	if err != nil {
		return err
	}
	if consumed {
		return domain.ErrApprovalConsumed
	}
	return nil
}

func (s *SessionService) prepareAuditAndApproval(ctx context.Context, decision policy.Decision, op domain.Operation, approval *domain.Approval, now time.Time) error {
	if s.auditStore != nil {
		if err := s.auditStore.RecordAdmissionIntentContext(ctx, op); err != nil {
			return err
		}
	}
	if decision.EffectiveClass.RequiresApproval() && approval != nil && s.approvalStore != nil {
		if err := s.approvalStore.MarkConsumedContext(ctx, *approval, now); err != nil {
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
	ctx context.Context,
	op domain.Operation,
	safetyRes SafetyResolution,
	approval *domain.Approval,
	now time.Time,
	fp, idFp domain.Fingerprint,
) (policy.Decision, *domain.Receipt, error) {
	decision, pErr := s.evaluatePolicy(ctx, op, safetyRes.RollbackState, approval, now)
	if pErr != nil {
		if err := ctx.Err(); err != nil {
			return decision, nil, err
		}
		rcpt, persistErr := s.persistOutcome(ctx, op, fp, idFp, decision, now, now, pErr, "", nil, 7, false)
		return decision, &rcpt, errors.Join(pErr, persistErr)
	}

	if decision.EffectiveClass.RequiresApproval() {
		if aErr := s.checkDestructiveApproval(ctx, approval); aErr != nil {
			rcpt, persistErr := s.persistOutcome(ctx, op, fp, idFp, decision, now, now, aErr, "", nil, 7, false)
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
		persistErr = s.persistTerminalOutcomeContext(finalizationCtx, rcpt)
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
		return nil, nil, errors.Join(err, releaseLease())
	}
	if ctx.Err() != nil {
		return &admittedSessionMutation{
			op: op, fp: fp, idFp: idFp,
			decision:     policy.Decision{Type: policy.DecisionAllow, EffectiveClass: op.Classification},
			safety:       safety,
			releaseLease: releaseLease,
		}, nil, nil
	}
	decision, denialReceipt, err := s.evaluateAndAdmit(ctx, op, safety, approval, now, fp, idFp)
	if err != nil {
		return nil, denialReceipt, errors.Join(err, releaseLease())
	}
	return &admittedSessionMutation{
		op: op, fp: fp, idFp: idFp, decision: decision, safety: safety, releaseLease: releaseLease,
	}, nil, nil
}

func (s *SessionService) reserveAndPrepareMutation(ctx context.Context, admitted *admittedSessionMutation, approval *domain.Approval) error {
	if s.mutationJournal == nil {
		return errors.Join(errors.New("app: session mutation journal is unavailable"), admitted.releaseLease())
	}
	now := s.now()
	if _, err := s.mutationJournal.ReserveContext(ctx, admitted.op, now); err != nil {
		return errors.Join(err, admitted.releaseLease())
	}
	if err := s.prepareAuditAndApproval(ctx, admitted.decision, admitted.op, approval, now); err != nil {
		cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		cancelErr := s.mutationJournal.CancelContext(cancelCtx, admitted.op)
		cancel()
		return errors.Join(err, cancelErr, admitted.releaseLease())
	}
	return nil
}

func (s *SessionService) releaseUnexecutedApproval(ctx context.Context, admitted *admittedSessionMutation, approval *domain.Approval, result sessionMutationResult) error {
	if approval == nil || !admitted.decision.EffectiveClass.RequiresApproval() || result.EffectApplied {
		return nil
	}
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return s.approvalStore.ReleaseUnexecutedContext(releaseCtx, *approval)
}

func validateSessionApprovalInput(approval *domain.Approval, approvalID string) error {
	if approval != nil && approvalID != "" {
		return fmt.Errorf("%w: approval and approval_id are mutually exclusive", domain.ErrInvalidApprovalRecord)
	}
	if approvalID == "" {
		return nil
	}
	return domain.ValidateApprovalID(approvalID)
}

func (s *SessionService) loadSessionApprovalReference(ctx context.Context, approvalID string) (*domain.Approval, error) {
	if approvalID == "" {
		return nil, nil
	}
	if s.approvalStore == nil {
		return nil, &PolicyDeniedError{Reason: policy.DenialApprovalMismatch, Message: "server-issued approval reference is invalid"}
	}
	loaded, err := s.approvalStore.LoadIssuedContext(ctx, approvalID)
	if err != nil {
		return nil, &PolicyDeniedError{Reason: policy.DenialApprovalMismatch, Message: "server-issued approval reference is invalid"}
	}
	return loaded, nil
}

func (s *SessionService) resolveSessionMutationApproval(
	ctx context.Context,
	op domain.Operation,
	initialFP, idFp domain.Fingerprint,
	approval *domain.Approval,
	approvalID string,
) (*domain.Approval, *domain.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if approval != nil && !op.Actor.HasScope(domain.ScopeSessionAdmin) {
		now := s.now()
		denial := &PolicyDeniedError{
			Reason:  policy.DenialApprovalRequired,
			Message: "raw approval objects require an authenticated session administrator",
		}
		decision := policy.Decision{Type: policy.DecisionDeny, EffectiveClass: op.Classification}
		rcpt, persistErr := s.persistOutcome(ctx, op, initialFP, idFp, decision, now, now, denial, "", nil, 7, false)
		return nil, &rcpt, errors.Join(denial, persistErr)
	}
	loaded, err := s.loadSessionApprovalReference(ctx, approvalID)
	if err != nil {
		return nil, nil, err
	}
	if loaded != nil {
		return loaded, nil, nil
	}
	return approval, nil, nil
}

func (s *SessionService) lookupCoordinatedSessionRetry(
	ctx context.Context,
	op domain.Operation,
	entry *inFlightSessionCall,
) (sessionMutationResult, *domain.Receipt, bool, error) {
	n, obs, rcpt, handled, retryErr := s.lookupSessionRetry(ctx, op)
	if !handled {
		return sessionMutationResult{}, nil, false, nil
	}
	result := sessionMutationResult{BytesWritten: n, Observation: obs}
	entry.result = result
	if rcpt != nil {
		entry.rcpt = *rcpt
		entry.hasReceipt = true
	}
	entry.err = retryErr
	return result, rcpt, true, retryErr
}

func (s *SessionService) coordinateSessionMutation(
	ctx context.Context,
	op domain.Operation,
	flightKey string,
	approval *domain.Approval,
	approvalID string,
	timeout time.Duration,
	execute func(context.Context) (sessionMutationResult, error),
) (sessionMutationResult, *domain.Receipt, error) {
	if err := validateSessionApprovalInput(approval, approvalID); err != nil {
		return sessionMutationResult{}, nil, err
	}
	initialFP, idFp, err := s.validateAndFingerprint(op)
	if err != nil {
		return sessionMutationResult{}, nil, err
	}
	leaseFP, err := mutationLeaseFingerprint(op)
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
	if result, rcpt, handled, retryErr := s.lookupCoordinatedSessionRetry(ctx, op, entry); handled {
		return result, rcpt, retryErr
	}
	approval, approvalRcpt, err := s.resolveSessionMutationApproval(ctx, op, initialFP, idFp, approval, approvalID)
	if err != nil {
		entry.err = err
		if approvalRcpt != nil {
			entry.rcpt = *approvalRcpt
			entry.hasReceipt = true
		}
		return sessionMutationResult{}, approvalRcpt, err
	}

	admitted, denialRcpt, err := s.admitSessionMutation(ctx, op, approval, timeout, leaseFP)
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
	mutErr = errors.Join(mutErr, s.releaseUnexecutedApproval(ctx, admitted, approval, result))
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
