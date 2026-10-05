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

// ExecuteLabMutation verifies live grant authority, then uses normal approval, policy,
// idempotency, host lease, audit, and receipt admission for a guest-only action.
func (s *ConsoleService) ExecuteLabMutation(ctx context.Context, actor domain.ActorContext, id string, op domain.Operation, req MutationRequest, providerID string, execFn func(context.Context) error) (result domain.Receipt, resultErr error) {
	return s.executeLabMutation(ctx, actor, id, op, req, providerID, execFn, nil)
}

// resolution is only the fresh server resolution from this desktop action. The final
// fenced authority check always resolves again, including enrollment and rollback.
func (s *ConsoleService) executeLabMutation(ctx context.Context, actor domain.ActorContext, id string, op domain.Operation, req MutationRequest, providerID string, execFn func(context.Context) error, resolution *TargetResolution) (result domain.Receipt, resultErr error) {
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
	binding, err := s.resolveLabBindingWithResolution(ctx, grant, resolution)
	if err == nil {
		err = s.labActivation(ctx, grant.GrantID)
	}
	if err != nil {
		return result, err
	}
	if !op.Deadline.After(s.recovery.now()) || op.Deadline.After(grant.ExpiresAt) {
		return result, ErrInvalidConsoleLabGrant
	}
	if binding.ProviderVMID != providerID {
		return result, ErrInvalidConsoleLabGrant
	}
	issued, err := s.deriveLabApproval(ctx, grant, op)
	if err != nil {
		return result, err
	}
	req.Approval, req.ApprovalID = issued, string(issued.ID)
	req.Timeout = 5 * time.Minute
	result, resultErr = s.recovery.executeMutation(ctx, op, req, providerID, func(execCtx context.Context) error {
		// Fence epoch-changing publication across the final authority check and guest dispatch.
		return s.target.WithEnrollmentFence(execCtx, func() error {
			if err := s.requireActiveLabGrant(execCtx, grant); err != nil {
				return err
			}
			return execFn(execCtx)
		})
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
