package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
)

func (s *RecoveryService) buildOperation(
	kind domain.OperationKind,
	req MutationRequest,
	initialClass domain.OperationClass,
	capability domain.Capability,
	params map[string]any,
) (domain.Operation, error) {
	deadline := req.Deadline
	if deadline.IsZero() {
		if req.Approval != nil && !req.Approval.ExpiresAt.IsZero() {
			deadline = req.Approval.ExpiresAt
		} else {
			deadline = s.now().Add(req.Timeout)
		}
	}

	op := domain.Operation{
		Kind:                kind,
		Target:              domain.MachineRef(req.TargetID),
		Actor:               req.Actor,
		Reason:              req.Reason,
		Deadline:            deadline,
		IdempotencyKey:      req.IdempotencyKey,
		RequiredCapability:  string(capability),
		RequiredScopes:      []string{"machine:write"},
		Classification:      initialClass,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters:          params,
	}

	if err := op.Validate(); err != nil {
		return domain.Operation{}, err
	}

	return op, nil
}

func (s *RecoveryService) validateDependencies() error {
	if s.backend == nil {
		return ErrMissingBackend
	}
	if s.leaseManager == nil || s.auditStore == nil || s.receiptStore == nil || s.approvalStore == nil {
		return errors.New("app: missing required recovery storage dependencies")
	}
	return nil
}

func (s *RecoveryService) finalizeMutation(
	receiptRecord domain.Receipt,
	runErr, persistErr, releaseErr error,
) (domain.Receipt, error) {
	if runErr != nil {
		if persistErr != nil || releaseErr != nil {
			return receiptRecord, errors.Join(runErr, persistErr, releaseErr)
		}
		return receiptRecord, runErr
	}

	if persistErr != nil || releaseErr != nil {
		return receiptRecord, fmt.Errorf("app: mutation succeeded but durable finalization failed: %w", errors.Join(persistErr, releaseErr))
	}

	return receiptRecord, nil
}

func (s *RecoveryService) executeMutation(
	ctx context.Context,
	op domain.Operation,
	req MutationRequest,
	providerTargetID string,
	execFn func(context.Context) error,
) (domain.Receipt, error) {
	execCtx, cancelExecution := s.mutationExecutionContext(ctx, op, req)
	defer cancelExecution()
	ctx = execCtx

	if err := s.validateDependencies(); err != nil {
		return domain.Receipt{}, err
	}

	fp, err := op.Fingerprint()
	if err != nil {
		return domain.Receipt{}, err
	}

	// 1. Idempotency Check & 2. Audit Writability Check
	if cached, err := s.checkPreconditions(ctx, op); err != nil || cached != nil {
		if cached != nil {
			switch cached.Outcome.Status {
			case domain.OutcomeDenied:
				return *cached, &PolicyDeniedError{
					Reason:  policy.DenialReason(cached.Outcome.ErrorCategory),
					Message: cached.Outcome.ErrorMessage,
				}
			case domain.OutcomeAborted:
				if cached.Outcome.ErrorCategory == "caller_canceled" {
					return *cached, context.Canceled
				}
				return *cached, context.DeadlineExceeded
			}
			return *cached, nil
		}
		return s.preProviderFailure(ctx, op, fp, policy.Decision{}, s.now(), err, "", req.ApprovalID)
	}

	now := s.now()

	// 3. Rollback discovery & 4. Policy evaluation & 5. Approval check
	decision, rollbackRef, err := s.prepareAndAuthorizeMutation(ctx, op, req, providerTargetID, now)
	if err != nil {
		var deniedErr *PolicyDeniedError
		if errors.As(err, &deniedErr) {
			finalizationCtx, cancel := boundedMutationFinalizationContext(ctx)
			defer cancel()
			receiptRecord, persistErr := s.persistOutcome(finalizationCtx, op, fp, decision, now, now, err, rollbackRef, req.ApprovalID)
			return s.finalizeMutation(receiptRecord, err, persistErr, nil)
		}
		return s.preProviderFailure(ctx, op, fp, decision, now, err, rollbackRef, req.ApprovalID)
	}

	// 6. Host Lease Acquisition
	releaseLease, err := s.acquireMutationLease(ctx, op, req, providerTargetID, fp)
	if err != nil {
		return s.preProviderFailure(ctx, op, fp, decision, now, err, rollbackRef, req.ApprovalID)
	}

	// 7. Audit Admission Intent & Approval Consumption
	approvalConsumed, err := s.recordAdmissionAndConsume(ctx, op, decision, req, now)
	if err != nil {
		return s.preProviderFailure(ctx, op, fp, decision, now, errors.Join(err, releaseLease()), rollbackRef, req.ApprovalID)
	}

	// 8. Lifecycle hooks & Provider Execution
	if err := runLifecycleHooks(ctx, req); err != nil {
		abortErr := s.compensatePreProviderAbort(ctx, req, approvalConsumed, releaseLease, err)
		return s.preProviderFailure(ctx, op, fp, decision, now, abortErr, rollbackRef, req.ApprovalID)
	}
	if err := ctx.Err(); err != nil {
		abortErr := s.compensatePreProviderAbort(ctx, req, approvalConsumed, releaseLease, err)
		return s.preProviderFailure(ctx, op, fp, decision, now, abortErr, rollbackRef, req.ApprovalID)
	}

	startedAt, completedAt, runErr := s.runProviderExecution(ctx, execFn)

	// 9. Receipt Persistence & Terminal Audit
	finalizationCtx, cancelFinalization := boundedMutationFinalizationContext(ctx)
	defer cancelFinalization()
	receiptRecord, persistErr := s.persistOutcome(finalizationCtx, op, fp, decision, startedAt, completedAt, runErr, rollbackRef, req.ApprovalID)

	// 10. Lease Release
	releaseErr := releaseLease()

	return s.finalizeMutation(receiptRecord, runErr, persistErr, releaseErr)
}

func (s *RecoveryService) checkPreconditions(ctx context.Context, op domain.Operation) (*domain.Receipt, error) {
	if s.receiptStore != nil {
		// Durable terminal truth wins over caller cancellation for an exact retry.
		lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		cached, err := s.receiptStore.LookupIdempotencyContext(lookupCtx, op)
		cancel()
		if err != nil {
			return nil, err
		}
		if cached != nil {
			return cached, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.auditStore != nil {
		if err := s.auditStore.CheckWritableContext(ctx); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrAuditUnavailable, err)
		}
	}
	return nil, nil
}

func (s *RecoveryService) acquireMutationLease(
	ctx context.Context,
	op domain.Operation,
	req MutationRequest,
	providerTargetID string,
	fp domain.Fingerprint,
) (func() error, error) {
	if s.leaseManager == nil {
		return func() error { return nil }, nil
	}
	leaseTTL := req.Timeout + 15*time.Second
	l, err := s.leaseManager.Acquire(ctx, providerTargetID, string(op.Kind), string(fp), leaseTTL)
	if err != nil {
		return nil, err
	}
	release := func() error {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return s.leaseManager.Release(cleanupCtx, l)
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, release())
	}
	return release, nil
}

func (s *RecoveryService) recordAdmissionAndConsume(
	ctx context.Context,
	op domain.Operation,
	decision policy.Decision,
	req MutationRequest,
	now time.Time,
) (bool, error) {
	if s.auditStore != nil {
		if err := s.auditStore.RecordAdmissionIntentContext(ctx, op); err != nil {
			return false, fmt.Errorf("%w: %w", ErrAuditUnavailable, err)
		}
	}
	if decision.EffectiveClass.RequiresApproval() && req.Approval != nil && s.approvalStore != nil {
		if err := s.approvalStore.MarkConsumedContext(ctx, *req.Approval, now); err != nil {
			return false, fmt.Errorf("app: failed to record approval consumption: %w", err)
		}
		return true, nil
	}
	return false, nil
}

func (s *RecoveryService) compensatePreProviderAbort(
	ctx context.Context,
	req MutationRequest,
	approvalConsumed bool,
	releaseLease func() error,
	primary error,
) error {
	var compensationErr error
	if approvalConsumed && req.Approval != nil && s.approvalStore != nil {
		compensationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		compensationErr = s.approvalStore.ReleaseUnexecutedContext(compensationCtx, *req.Approval)
		cancel()
		if compensationErr != nil {
			compensationErr = fmt.Errorf("app: failed to persist pre-provider approval compensation: %w", compensationErr)
		}
	}
	return errors.Join(primary, compensationErr, releaseLease())
}

func (s *RecoveryService) discoverRollback(ctx context.Context, op domain.Operation, targetID string) (policy.RollbackState, string, error) {
	if op.Classification != domain.ClassReversibleMutation {
		return policy.RollbackState{}, "", nil
	}
	checkpoints, listErr := s.backend.ListCheckpoints(ctx, targetID)
	if err := ctx.Err(); err != nil {
		return policy.RollbackState{}, "", err
	}
	if errors.Is(listErr, context.Canceled) || errors.Is(listErr, context.DeadlineExceeded) {
		return policy.RollbackState{}, "", listErr
	}
	if listErr != nil || len(checkpoints) == 0 {
		return policy.RollbackState{}, "", nil
	}

	var valid []domain.CheckpointObservation
	for _, chk := range checkpoints {
		if err := chk.Validate(); err != nil {
			return policy.RollbackState{}, "", nil
		}
		if chk.VMID != targetID {
			return policy.RollbackState{}, "", nil
		}
		valid = append(valid, chk)
	}
	if len(valid) == 0 {
		return policy.RollbackState{}, "", nil
	}

	// Deterministic selection: newest CreatedAt, breaking ties with lexicographical ID
	sort.Slice(valid, func(i, j int) bool {
		if valid[i].CreatedAt.Equal(valid[j].CreatedAt) {
			return valid[i].ID < valid[j].ID
		}
		return valid[i].CreatedAt.After(valid[j].CreatedAt)
	})

	ref := valid[0].ID
	return policy.RollbackState{
		Available:    true,
		Verified:     true,
		CheckpointID: ref,
	}, ref, nil
}

func (s *RecoveryService) evaluateMutationPolicy(
	ctx context.Context,
	op domain.Operation,
	req MutationRequest,
	providerTargetID string,
	now time.Time,
	rollback policy.RollbackState,
) (policy.Decision, error) {
	caps, err := s.backend.Capabilities(ctx, providerTargetID)
	if err != nil {
		return policy.Decision{}, fmt.Errorf("app: failed to retrieve backend capabilities: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return policy.Decision{}, err
	}

	auditWritable := s.auditStore != nil && s.auditStore.CheckWritableContext(ctx) == nil
	if err := ctx.Err(); err != nil {
		return policy.Decision{}, err
	}

	evalInput := policy.EvaluationInput{
		Operation:               op,
		Now:                     now,
		AuditWritable:           auditWritable,
		RollbackState:           rollback,
		Approval:                req.Approval,
		RollbackPolicy:          policy.RollbackPolicyEscalateToDestructive,
		AvailableCapabilities:   caps,
		SensitiveEvidenceScopes: domain.NewScopeSet("evidence:sensitive"),
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

func (s *RecoveryService) verifyApprovalUnconsumed(ctx context.Context, decision policy.Decision, req MutationRequest) error {
	if !decision.EffectiveClass.RequiresApproval() {
		return nil
	}
	if req.Approval == nil {
		return &PolicyDeniedError{
			Reason:  policy.DenialApprovalRequired,
			Message: "destructive/privileged operation requires operator approval",
		}
	}
	if s.approvalStore != nil {
		consumed, err := s.approvalStore.IsConsumedContext(ctx, string(req.Approval.ID))
		if err != nil {
			return fmt.Errorf("app: failed to verify approval consumption: %w", err)
		}
		if consumed {
			return approvalActivityDenial(domain.ErrApprovalConsumed)
		}
	}
	return nil
}

func (s *RecoveryService) persistOutcome(
	ctx context.Context,
	op domain.Operation,
	fp domain.Fingerprint,
	decision policy.Decision,
	startedAt, completedAt time.Time,
	runErr error,
	rollbackRef string,
	approvalID string,
) (domain.Receipt, error) {
	outcomeStatus := domain.OutcomeSuccess
	exitCode := 0
	effectiveRollback := rollbackRef

	var errCategory, errMsg string

	if runErr != nil {
		outcomeStatus = domain.OutcomeFailed
		exitCode = 1
		effectiveRollback = ""

		var deniedErr *PolicyDeniedError
		switch {
		case errors.As(runErr, &deniedErr):
			outcomeStatus = domain.OutcomeDenied
			exitCode = 7
			errCategory = string(deniedErr.Reason)
			errMsg = deniedErr.Message
		case errors.Is(runErr, context.DeadlineExceeded) || errors.Is(runErr, domain.ErrMissingDeadline):
			outcomeStatus = domain.OutcomeAborted
			errCategory = "deadline_exceeded"
			errMsg = "operation deadline exceeded"
		case errors.Is(runErr, context.Canceled):
			outcomeStatus = domain.OutcomeAborted
			errCategory = "caller_canceled"
			errMsg = "operation cancelled"
		}
	}

	rcptID, err := generateReceiptID()
	if err != nil {
		return domain.Receipt{}, fmt.Errorf("app: failed to generate receipt ID: %w", err)
	}

	idFp, err := domain.ComputeIdempotencyFingerprint(op)
	if err != nil {
		return domain.Receipt{}, fmt.Errorf("app: failed to compute idempotency fingerprint: %w", err)
	}

	receiptRecord := domain.Receipt{
		ReceiptID:              domain.ReceiptID(rcptID),
		OperationKind:          op.Kind,
		Fingerprint:            fp,
		IdempotencyFingerprint: idFp,
		IdempotencyKey:         op.IdempotencyKey,
		Actor:                  op.Actor.EffectiveActor,
		Target:                 op.Target,
		Class:                  decision.EffectiveClass,
		EffectiveBackend:       "hyperv",
		StartedAt:              startedAt,
		CompletedAt:            completedAt,
		Outcome: domain.ExecutionOutcome{
			Status:        outcomeStatus,
			ExitCode:      exitCode,
			ErrorCategory: errCategory,
			ErrorMessage:  errMsg,
		},
		ObservationType: domain.ObservationObserved,
		RollbackRef:     effectiveRollback,
		RedactionStatus: domain.RedactionApplied,
	}
	if approvalID != "" {
		receiptRecord.EvidenceRefs = []string{approvalID}
	}

	var saveErr, auditErr error
	if s.receiptStore != nil {
		saveErr = s.receiptStore.EnsureContext(ctx, receiptRecord)
	}
	if saveErr != nil {
		receiptRecord.ReceiptID = "" // zero/no receipt ID
		return receiptRecord, saveErr
	}

	if s.auditStore != nil {
		auditErr = s.auditStore.EnsureTerminalOutcomeContext(ctx, receiptRecord)
	}

	if auditErr != nil {
		return receiptRecord, auditErr
	}

	return receiptRecord, nil
}

func generateReceiptID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto/rand error: %w", err)
	}
	return fmt.Sprintf("rcpt-%s", hex.EncodeToString(b)), nil
}
