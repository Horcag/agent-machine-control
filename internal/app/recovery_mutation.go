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
	execFn func(context.Context) error,
) (domain.Receipt, error) {
	if err := s.validateDependencies(); err != nil {
		return domain.Receipt{}, err
	}

	fp, err := op.Fingerprint()
	if err != nil {
		return domain.Receipt{}, err
	}

	// 1. Idempotency Check & 2. Audit Writability Check
	if cached, err := s.checkPreconditions(op); err != nil || cached != nil {
		if cached != nil {
			return *cached, nil
		}
		return domain.Receipt{}, err
	}

	now := s.now()

	// 3. Rollback point discovery & 4. Policy evaluation
	rollbackState, rollbackRef := s.discoverRollback(ctx, op, req.TargetID)
	decision, err := s.evaluateMutationPolicy(ctx, op, req, now, rollbackState)
	if err != nil {
		return domain.Receipt{}, err
	}

	// 5. Approval Verification Check
	if err := s.verifyApprovalUnconsumed(decision, req); err != nil {
		return domain.Receipt{}, err
	}

	// 6. Host Lease Acquisition
	releaseLease, err := s.acquireMutationLease(ctx, op, req, fp)
	if err != nil {
		return domain.Receipt{}, err
	}

	// 7. Audit Admission Intent & Approval Consumption
	if err := s.recordAdmissionAndConsume(op, decision, req, now); err != nil {
		if relErr := releaseLease(); relErr != nil {
			return domain.Receipt{}, errors.Join(err, relErr)
		}
		return domain.Receipt{}, err
	}

	// 8. Provider Execution
	startedAt, completedAt, runErr := s.runProviderExecution(ctx, req, execFn)

	// 9. Receipt Persistence & Terminal Audit
	receiptRecord, persistErr := s.persistOutcome(op, fp, decision, startedAt, completedAt, runErr, rollbackRef)

	// 10. Lease Release
	releaseErr := releaseLease()

	return s.finalizeMutation(receiptRecord, runErr, persistErr, releaseErr)
}

func (s *RecoveryService) checkPreconditions(op domain.Operation) (*domain.Receipt, error) {
	if s.receiptStore != nil {
		cached, err := s.receiptStore.LookupIdempotency(op)
		if err != nil {
			return nil, err
		}
		if cached != nil {
			return cached, nil
		}
	}
	if s.auditStore != nil {
		if err := s.auditStore.CheckWritable(); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrAuditUnavailable, err)
		}
	}
	return nil, nil
}

func (s *RecoveryService) acquireMutationLease(
	ctx context.Context,
	op domain.Operation,
	req MutationRequest,
	fp domain.Fingerprint,
) (func() error, error) {
	if s.leaseManager == nil {
		return func() error { return nil }, nil
	}
	leaseTTL := req.Timeout + 15*time.Second
	l, err := s.leaseManager.Acquire(ctx, req.TargetID, string(op.Kind), string(fp), leaseTTL)
	if err != nil {
		return nil, err
	}
	return func() error {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return s.leaseManager.Release(cleanupCtx, l)
	}, nil
}

func (s *RecoveryService) recordAdmissionAndConsume(
	op domain.Operation,
	decision policy.Decision,
	req MutationRequest,
	now time.Time,
) error {
	if s.auditStore != nil {
		if err := s.auditStore.RecordAdmissionIntent(op); err != nil {
			return fmt.Errorf("%w: %v", ErrAuditUnavailable, err)
		}
	}
	if decision.EffectiveClass.RequiresApproval() && req.Approval != nil && s.approvalStore != nil {
		if err := s.approvalStore.MarkConsumed(*req.Approval, now); err != nil {
			return fmt.Errorf("app: failed to record approval consumption: %w", err)
		}
	}
	return nil
}

func (s *RecoveryService) runProviderExecution(
	ctx context.Context,
	req MutationRequest,
	execFn func(context.Context) error,
) (time.Time, time.Time, error) {
	startedAt := s.now()
	execCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	runErr := execFn(execCtx)
	completedAt := s.now()
	if !completedAt.After(startedAt) {
		completedAt = startedAt.Add(time.Millisecond)
	}
	return startedAt, completedAt, runErr
}

func (s *RecoveryService) discoverRollback(ctx context.Context, op domain.Operation, targetID string) (policy.RollbackState, string) {
	if op.Classification != domain.ClassReversibleMutation {
		return policy.RollbackState{}, ""
	}
	checkpoints, listErr := s.backend.ListCheckpoints(ctx, targetID)
	if listErr != nil || len(checkpoints) == 0 {
		return policy.RollbackState{}, ""
	}

	var valid []domain.CheckpointObservation
	for _, chk := range checkpoints {
		if err := chk.Validate(); err != nil {
			return policy.RollbackState{}, ""
		}
		if chk.VMID != targetID {
			return policy.RollbackState{}, ""
		}
		valid = append(valid, chk)
	}
	if len(valid) == 0 {
		return policy.RollbackState{}, ""
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
	}, ref
}

func (s *RecoveryService) evaluateMutationPolicy(
	ctx context.Context,
	op domain.Operation,
	req MutationRequest,
	now time.Time,
	rollback policy.RollbackState,
) (policy.Decision, error) {
	caps, err := s.backend.Capabilities(ctx, req.TargetID)
	if err != nil {
		return policy.Decision{}, fmt.Errorf("app: failed to retrieve backend capabilities: %w", err)
	}

	auditWritable := s.auditStore != nil && s.auditStore.CheckWritable() == nil

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

func (s *RecoveryService) verifyApprovalUnconsumed(decision policy.Decision, req MutationRequest) error {
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
		consumed, err := s.approvalStore.IsConsumed(string(req.Approval.ID))
		if err != nil {
			return fmt.Errorf("app: failed to verify approval consumption: %w", err)
		}
		if consumed {
			return domain.ErrApprovalConsumed
		}
	}
	return nil
}

func (s *RecoveryService) persistOutcome(
	op domain.Operation,
	fp domain.Fingerprint,
	decision policy.Decision,
	startedAt, completedAt time.Time,
	runErr error,
	rollbackRef string,
) (domain.Receipt, error) {
	outcomeStatus := domain.OutcomeSuccess
	exitCode := 0
	effectiveRollback := rollbackRef

	if runErr != nil {
		outcomeStatus = domain.OutcomeFailed
		exitCode = 1
		effectiveRollback = ""
		if errors.Is(runErr, context.DeadlineExceeded) || errors.Is(runErr, domain.ErrMissingDeadline) {
			outcomeStatus = domain.OutcomeAborted
		}
	}

	rcptID, err := generateReceiptID()
	if err != nil {
		return domain.Receipt{}, fmt.Errorf("app: failed to generate receipt ID: %w", err)
	}

	receiptRecord := domain.Receipt{
		ReceiptID:        domain.ReceiptID(rcptID),
		OperationKind:    op.Kind,
		Fingerprint:      fp,
		IdempotencyKey:   op.IdempotencyKey,
		Actor:            op.Actor.EffectiveActor,
		Target:           op.Target,
		Class:            decision.EffectiveClass,
		EffectiveBackend: "hyperv",
		StartedAt:        startedAt,
		CompletedAt:      completedAt,
		Outcome: domain.ExecutionOutcome{
			Status:   outcomeStatus,
			ExitCode: exitCode,
		},
		ObservationType: domain.ObservationObserved,
		RollbackRef:     effectiveRollback,
		RedactionStatus: domain.RedactionApplied,
	}

	var saveErr, auditErr error
	if s.receiptStore != nil {
		saveErr = s.receiptStore.Save(receiptRecord)
	}
	if s.auditStore != nil {
		auditErr = s.auditStore.RecordTerminalOutcome(receiptRecord)
	}

	if saveErr != nil || auditErr != nil {
		return receiptRecord, errors.Join(saveErr, auditErr)
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
