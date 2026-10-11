package app

import (
	"context"
	"fmt"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

// IssueApproval uses the existing operator-only issuance, immutable approval
// store, and redacted receipt contract for one exact whole guest operation.
func (s *GuestService) IssueApproval(ctx context.Context, p GuestOperationParams, validFor time.Duration) (*SessionApprovalGrant, *domain.Receipt, error) {
	return s.IssueApprovalFor(ctx, p, validFor, string(sessionApprovalBeneficiary))
}

// IssueApprovalFor supports only the current operator or the canonical MCP agent.
func (s *GuestService) IssueApprovalFor(ctx context.Context, p GuestOperationParams, validFor time.Duration, beneficiary string) (*SessionApprovalGrant, *domain.Receipt, error) {
	if s == nil || s.session == nil {
		return nil, nil, ErrGuestAuthority
	}
	if err := s.session.validateSessionApprovalIssue(SessionApprovalIssueParams{Caller: p.Caller, ValidFor: validFor}); err != nil {
		return nil, nil, err
	}
	issuer := p.Caller
	selected, err := guestApprovalBeneficiary(issuer, beneficiary)
	if err != nil {
		return nil, nil, err
	}
	p.Caller = selected
	resolution, config, err := s.resolve(ctx, p)
	if err != nil {
		return nil, nil, err
	}
	p.Target = resolution.Locator.String()
	s.session.issueMu.Lock()
	defer s.session.issueMu.Unlock()
	existing, err := s.session.loadIssuedSessionApproval(ctx, p.IdempotencyKey)
	if err != nil {
		return nil, nil, err
	}
	issuedAt, deadline := s.session.sessionApprovalTimes(validFor, existing)
	p.Deadline = deadline
	op, err := guestOperation(p, domain.MachineRef(p.Target), config)
	if err != nil {
		return nil, nil, err
	}
	op.Classification = s.session.resolveSafety(ctx, op.Target).Classification
	if !op.Classification.RequiresApproval() {
		return nil, nil, fmt.Errorf("%w: guest operation does not require approval", domain.ErrSessionAccessDenied)
	}
	fp, _, err := s.session.validateAndFingerprint(op)
	if err != nil {
		return nil, nil, err
	}
	issued := domain.Approval{ID: domain.ApprovalID(deriveSessionApprovalID(p.IdempotencyKey)), Actor: op.Actor.EffectiveActor, Target: op.Target, AuthorizedClass: op.Classification, Fingerprint: fp, IdempotencyKey: op.IdempotencyKey, IssuedAt: issuedAt, ExpiresAt: deadline}
	if existing != nil && !equalIssuedSessionApproval(*existing, issued) {
		return nil, nil, receipt.ErrIdempotencyCollision
	}
	issuanceOp, rcpt, err := buildSessionApprovalIssuanceEvidence(issuer, op, issued)
	if err != nil {
		return nil, nil, err
	}
	if err = s.session.persistIssuedSessionApproval(ctx, existing, issued, issuanceOp, rcpt); err != nil {
		return nil, &rcpt, err
	}
	return sessionApprovalGrant(issued, op), &rcpt, nil
}

func guestApprovalBeneficiary(issuer domain.ActorContext, beneficiary string) (domain.ActorContext, error) {
	switch beneficiary {
	case "self":
		return issuer.Clone(), nil
	case string(sessionApprovalBeneficiary):
		agent, err := sessionApprovalAgentActor()
		if err != nil {
			return agent, err
		}
		agent.CallerPermissions[domain.ScopeEvidenceCapture] = struct{}{}
		agent.EffectivePermissions[domain.ScopeEvidenceCapture] = struct{}{}
		return agent, nil
	default:
		return domain.ActorContext{}, domain.ErrSessionAccessDenied
	}
}
