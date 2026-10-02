package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func (s *ConsoleService) labInput(ctx context.Context, actor domain.ActorContext, req ConsoleInputRequest) (domain.Receipt, error) {
	if req.ApprovalID != "" || req.Input.Validate() != nil {
		return domain.Receipt{}, ErrInvalidConsoleLabGrant
	}
	if err := s.validateDependencies(); err != nil {
		return domain.Receipt{}, err
	}
	deadline, err := time.Parse(time.RFC3339Nano, req.Deadline)
	if err != nil {
		return domain.Receipt{}, domain.ErrMissingDeadline
	}
	canonical, providerID, err := s.recovery.resolveTargetReference(ctx, req.Target)
	if err != nil {
		return domain.Receipt{}, err
	}
	mut := MutationRequest{TargetID: canonical, Actor: actor, Reason: req.Reason, IdempotencyKey: req.IdempotencyKey, Deadline: deadline, Timeout: 5 * time.Minute}
	op, err := s.recovery.buildOperation("console.input", mut, domain.ClassDestructivePrivileged, domain.CapabilityConsoleInput, domain.ConsoleInputParameters(req.Input))
	if err != nil {
		return domain.Receipt{}, err
	}
	return s.ExecuteLabMutation(ctx, actor, req.LabGrantID, op, mut, providerID, func(execCtx context.Context) error {
		input, err := s.resolveFrameInput(execCtx, canonical, providerID, req.Input)
		if err != nil {
			return err
		}
		if err := s.provider.SendConsoleInput(execCtx, providerID, input); err != nil {
			return safeConsoleProviderError(err)
		}
		return nil
	})
}

// ExecuteLabMutation verifies live grant authority, then uses normal approval, policy,
// idempotency, host lease, audit, and receipt admission for a guest-only action.
func (s *ConsoleService) ExecuteLabMutation(ctx context.Context, actor domain.ActorContext, id string, op domain.Operation, req MutationRequest, providerID string, execFn func(context.Context) error) (result domain.Receipt, resultErr error) {
	if err := validateLabMutation(actor, op, req, execFn); err != nil {
		return result, err
	}
	// The grant lease must outlive admission, guest execution, and finalization together.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	release, err := s.labLock(ctx, id)
	if err != nil {
		return result, err
	}
	defer func() { resultErr = errors.Join(resultErr, release()) }()
	grant, err := s.loadLabGrant(ctx, id)
	if err != nil || grant.Beneficiary != actor.EffectiveActor || op.Target != grant.Target {
		return result, ErrInvalidConsoleLabGrant
	}
	if err := s.requireActiveLabGrant(ctx, grant); err != nil {
		return result, err
	}
	if !op.Deadline.After(s.recovery.now()) || op.Deadline.After(grant.ExpiresAt) {
		return result, ErrInvalidConsoleLabGrant
	}
	resolution, err := s.target.ResolveTarget(ctx, string(op.Target))
	if err != nil || resolution.ProviderVMID != providerID {
		return result, ErrInvalidConsoleLabGrant
	}
	issued, err := s.deriveLabApproval(ctx, grant, op)
	if err != nil {
		return result, err
	}
	req.Approval, req.ApprovalID = issued, string(issued.ID)
	req.Timeout = 5 * time.Minute
	result, resultErr = s.recovery.executeMutation(ctx, op, req, providerID, func(execCtx context.Context) error {
		// Recheck authority after ordinary admission has acquired the machine lease.
		if err := s.requireActiveLabGrant(execCtx, grant); err != nil {
			return err
		}
		return execFn(execCtx)
	})
	if resultErr == nil && result.Outcome.Status == domain.OutcomeFailed {
		resultErr = errors.New("app: guest lab mutation previously failed")
	}
	return result, resultErr
}

func labOperationAllowed(op domain.Operation) bool {
	allowed := (op.Kind == "console.input" && op.RequiredCapability == string(domain.CapabilityConsoleInput)) || (op.Kind == "desktop.action" && op.RequiredCapability == "desktop.action")
	return allowed &&
		op.Classification == domain.ClassDestructivePrivileged && len(op.RequiredScopes) == 1 && op.RequiredScopes[0] == domain.ScopeMachineWrite
}

func (s *ConsoleService) deriveLabApproval(ctx context.Context, grant ConsoleLabGrant, op domain.Operation) (*domain.Approval, error) {
	fingerprint, err := op.Fingerprint()
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(grant.GrantID + "\x00" + string(op.Actor.EffectiveActor) + "\x00" + op.IdempotencyKey))
	id := domain.ApprovalID(ConsoleLabApprovalPrefix + hex.EncodeToString(digest[:16]))
	existing, err := s.recovery.approvalStore.LoadIssuedContext(ctx, string(id))
	if err != nil && !errors.Is(err, approval.ErrApprovalNotIssued) {
		return nil, err
	}
	issued := domain.Approval{ID: id, Actor: op.Actor.EffectiveActor, Target: op.Target, AuthorizedClass: domain.ClassDestructivePrivileged,
		Fingerprint: fingerprint, IdempotencyKey: op.IdempotencyKey, IssuedAt: s.recovery.now(), ExpiresAt: op.Deadline}
	if existing != nil {
		issued.IssuedAt = existing.IssuedAt
		if !equalIssuedOperationApproval(*existing, issued) {
			return nil, receipt.ErrIdempotencyCollision
		}
	}
	// Issuance records preserve the operator principal that delegated this lab.
	scopes := domain.NewScopeSet(domain.ScopeOperationAdmin)
	issuer, err := domain.NewActorContext(grant.Issuer, grant.Issuer, scopes, scopes)
	if err != nil {
		return nil, err
	}
	issuanceOp, issuanceReceipt, err := buildOperationApprovalIssuanceEvidence(issuer, op, issued)
	if err != nil {
		return nil, err
	}
	if err := s.recovery.persistIssuedOperationApproval(ctx, existing, issued, issuanceOp, issuanceReceipt); err != nil {
		return nil, err
	}
	return &issued, nil
}

func validateLabMutation(actor domain.ActorContext, op domain.Operation, req MutationRequest, execFn func(context.Context) error) error {
	if actor.Validate() != nil || actor.IsDelegated() || execFn == nil || req.Approval != nil || req.ApprovalID != "" || req.ApprovalError != nil {
		return ErrInvalidConsoleLabGrant
	}
	if !labOperationAllowed(op) || op.Validate() != nil || domain.ValidateOperationParameters(op.Kind, op.Parameters) != nil || !labMutationIdentityMatches(actor, op, req) {
		return ErrInvalidConsoleLabGrant
	}
	if !actor.HasScope(domain.ScopeMachineWrite) || !op.Actor.HasScope(domain.ScopeMachineWrite) || !req.Actor.HasScope(domain.ScopeMachineWrite) {
		return ErrInvalidConsoleLabGrant
	}
	return nil
}

func labMutationIdentityMatches(actor domain.ActorContext, op domain.Operation, req MutationRequest) bool {
	return op.Actor.AuthenticatedCaller == actor.AuthenticatedCaller && op.Actor.EffectiveActor == actor.EffectiveActor &&
		op.Target == domain.MachineRef(req.TargetID) && op.Reason == req.Reason && op.IdempotencyKey == req.IdempotencyKey && op.Deadline.Equal(req.Deadline) &&
		req.Actor.AuthenticatedCaller == actor.AuthenticatedCaller && req.Actor.EffectiveActor == actor.EffectiveActor
}
