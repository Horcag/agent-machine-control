package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

// IssueLabGrant persists operator-authorized, VM-scoped console authority.
func (s *ConsoleService) IssueLabGrant(ctx context.Context, actor domain.ActorContext, req ConsoleLabGrantIssueRequest) (grant ConsoleLabGrant, result domain.Receipt, resultErr error) {
	beneficiary, err := validateLabIssueRequest(actor, req)
	if err != nil {
		return grant, result, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	id := labGrantID(actor.EffectiveActor, req.IdempotencyKey)
	release, err := s.labLock(ctx, id)
	if err != nil {
		return grant, result, err
	}
	defer func() { resultErr = errors.Join(resultErr, release()) }()
	resolution, identity, err := s.labIdentity(ctx, req.Target)
	if err != nil {
		return grant, result, err
	}
	if err := s.requireLabSafety(ctx, resolution, req.AcknowledgeExternalEffects); err != nil {
		return grant, result, err
	}
	existing, err := s.loadLabGrant(ctx, id)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return grant, result, err
	}
	now := s.recovery.now()
	grant = ConsoleLabGrant{AcknowledgeExternalEffects: req.AcknowledgeExternalEffects, SchemaVersion: 1, GrantID: id, Target: domain.MachineRef(resolution.Locator.String()), EnrollmentIdentity: identity,
		Issuer: actor.EffectiveActor, Beneficiary: beneficiary, Reason: req.Reason, IdempotencyKey: req.IdempotencyKey,
		IssuedAt: now, ExpiresAt: now.Add(time.Duration(req.ValidForMillis) * time.Millisecond)}
	if err == nil {
		grant.IssuedAt, grant.ExpiresAt = existing.IssuedAt, existing.ExpiresAt
		if grant != existing || existing.ExpiresAt.Sub(existing.IssuedAt).Milliseconds() != req.ValidForMillis {
			return ConsoleLabGrant{}, result, receipt.ErrIdempotencyCollision
		}
		if err := s.validateLabBinding(ctx, existing); err != nil {
			return ConsoleLabGrant{}, result, err
		}
	}
	op := labGrantOperation("console.lab.grant.issue", actor, grant, req.Reason, req.IdempotencyKey, grant.ExpiresAt)
	op.Parameters = map[string]any{"grant_id": id, "beneficiary": string(beneficiary), "enrollment_identity": identity, "expires_at": grant.ExpiresAt.UTC().Format(time.RFC3339Nano), "acknowledge_external_effects": grant.AcknowledgeExternalEffects}
	result, resultErr = s.persistLabAuthority(ctx, op, grant.IssuedAt, id, func() error {
		if err == nil {
			return nil
		}
		return s.writeLabDocument(ctx, id, ".json", grant)
	})
	if resultErr != nil {
		return ConsoleLabGrant{}, result, resultErr
	}
	if err := s.activateLabGrant(ctx, id); err != nil {
		return ConsoleLabGrant{}, result, err
	}
	return grant, result, nil
}

func labOperator(actor domain.ActorContext) error {
	if actor.Validate() != nil || actor.IsDelegated() || !actor.HasScope(domain.ScopeOperationAdmin) {
		return ErrOperationApprovalForbidden
	}
	return nil
}

func labBeneficiary(actor domain.ActorContext, beneficiary string) (domain.ActorID, error) {
	switch beneficiary {
	case "", "self":
		return actor.EffectiveActor, nil
	case string(operationApprovalMCPBeneficiary):
		return operationApprovalMCPBeneficiary, nil
	default:
		return "", ErrOperationApprovalForbidden
	}
}

func labGrantID(actor domain.ActorID, key string) string {
	digest := sha256.Sum256([]byte("console-lab-grant\x00" + string(actor) + "\x00" + key))
	return hex.EncodeToString(digest[:16])
}

func (s *ConsoleService) labIdentity(ctx context.Context, reference string) (TargetResolution, string, error) {
	if s.labEnrollment == nil || s.labSafety == nil {
		return TargetResolution{}, "", ErrInvalidConsoleLabGrant
	}
	resolution, err := s.target.ResolveTarget(ctx, reference)
	if err != nil {
		return resolution, "", err
	}
	identity, err := s.labEnrollment(ctx, domain.MachineRef(resolution.Locator.String()))
	if err != nil || !validLabDigest(identity) {
		return resolution, "", ErrInvalidConsoleLabGrant
	}
	return resolution, identity, nil
}

func (s *ConsoleService) requireLabSafety(ctx context.Context, resolution TargetResolution, acknowledge bool) error {
	safety, err := s.labSafety.ResolveSafety(ctx, domain.MachineRef(resolution.ProviderVMID))
	if err != nil || (!safety.Contained && !acknowledge) || !safety.RollbackState.Available || !safety.RollbackState.Verified || safety.RollbackRef == "" {
		return ErrInvalidConsoleLabGrant
	}
	return nil
}

func (s *ConsoleService) requireActiveLabGrant(ctx context.Context, grant ConsoleLabGrant) error {
	_, err := s.resolveActiveLabGrant(ctx, grant)
	return err
}

func (s *ConsoleService) validateLabBinding(ctx context.Context, grant ConsoleLabGrant) error {
	_, err := s.resolveLabBinding(ctx, grant)
	return err
}

func (s *ConsoleService) resolveActiveLabGrant(ctx context.Context, grant ConsoleLabGrant) (TargetResolution, error) {
	resolution, err := s.resolveLabBinding(ctx, grant)
	if err != nil {
		return TargetResolution{}, err
	}
	return resolution, s.labActivation(ctx, grant.GrantID)
}

func (s *ConsoleService) resolveLabBinding(ctx context.Context, grant ConsoleLabGrant) (TargetResolution, error) {
	return s.resolveLabBindingWithResolution(ctx, grant, nil)
}

func (s *ConsoleService) resolveLabBindingWithResolution(ctx context.Context, grant ConsoleLabGrant, observed *TargetResolution) (TargetResolution, error) {
	now := s.recovery.now()
	if now.Before(grant.IssuedAt) || !now.Before(grant.ExpiresAt) {
		return TargetResolution{}, ErrInvalidConsoleLabGrant
	}
	revoked, err := s.labRevoked(ctx, grant.GrantID)
	if err != nil || revoked {
		return TargetResolution{}, ErrInvalidConsoleLabGrant
	}
	var resolution TargetResolution
	var identity string
	if observed == nil {
		resolution, identity, err = s.labIdentity(ctx, string(grant.Target))
	} else {
		resolution = *observed
		if s.labEnrollment == nil || s.labSafety == nil || resolution.Validate() != nil {
			return TargetResolution{}, ErrInvalidConsoleLabGrant
		}
		identity, err = s.labEnrollment(ctx, domain.MachineRef(resolution.Locator.String()))
		if !validLabDigest(identity) {
			return TargetResolution{}, ErrInvalidConsoleLabGrant
		}
	}
	if err != nil || resolution.Locator.String() != string(grant.Target) || identity != grant.EnrollmentIdentity {
		return TargetResolution{}, ErrInvalidConsoleLabGrant
	}
	return resolution, s.requireLabSafety(ctx, resolution, grant.AcknowledgeExternalEffects)
}

func labGrantOperation(kind domain.OperationKind, actor domain.ActorContext, grant ConsoleLabGrant, reason, key string, deadline time.Time) domain.Operation {
	return domain.Operation{Kind: kind, Target: grant.Target, Actor: actor, Reason: reason, IdempotencyKey: key, Deadline: deadline,
		RequiredScopes: []string{domain.ScopeOperationAdmin}, RequiredCapability: string(kind), Classification: domain.ClassDestructivePrivileged,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard}
}

func (s *ConsoleService) persistLabAuthority(ctx context.Context, op domain.Operation, at time.Time, id string, persist func() error) (domain.Receipt, error) {
	if err := op.Validate(); err != nil {
		return domain.Receipt{}, err
	}
	if err := domain.ValidateOperationParameters(op.Kind, op.Parameters); err != nil {
		return domain.Receipt{}, err
	}
	issued := domain.Approval{ID: domain.ApprovalID("app-lab-grant-" + id), IssuedAt: at}
	result, err := buildApprovalIssuanceReceipt(string(op.Kind), op, issued)
	if err != nil {
		return result, err
	}
	if err := s.recovery.auditStore.RecordAdmissionIntentContext(ctx, op); err != nil {
		return result, err
	}
	if err := persist(); err != nil {
		return result, err
	}
	if err := s.recovery.receiptStore.EnsureContext(ctx, result); err != nil {
		return result, err
	}
	return result, s.recovery.auditStore.EnsureTerminalOutcomeContext(ctx, result)
}

func validateLabIssueRequest(actor domain.ActorContext, req ConsoleLabGrantIssueRequest) (domain.ActorID, error) {
	if err := labOperator(actor); err != nil {
		return "", err
	}
	if domain.ValidateReason(req.Reason) != nil || domain.ValidateIdempotencyKey(req.IdempotencyKey) != nil || req.ValidForMillis < 1000 || req.ValidForMillis > MaxConsoleLabGrantValidity.Milliseconds() {
		return "", ErrInvalidConsoleLabGrant
	}
	return labBeneficiary(actor, req.Beneficiary)
}
