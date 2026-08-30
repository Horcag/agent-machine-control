package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

const (
	sessionApprovalBeneficiary domain.ActorID = "agent:mcp-local"
	minSessionApprovalValidity                = time.Second
	maxSessionApprovalValidity                = 5 * time.Minute
)

// SessionApprovalIssueParams describes one operator-authorized session mutation approval.
// Data is used only to reconstruct the write hash and length and is never persisted.
type SessionApprovalIssueParams struct {
	Kind           domain.OperationKind
	Caller         domain.ActorContext
	Target         string
	SessionID      domain.SessionID
	Data           string
	Key            domain.ControlKey
	Reason         string
	IdempotencyKey string
	ValidFor       time.Duration
	Cols           uint16
	Rows           uint16
	Term           string
	Force          bool
}

// SessionApprovalSummary is the redacted exact operation identity returned to the operator.
type SessionApprovalSummary struct {
	Kind           domain.OperationKind `json:"kind"`
	Target         domain.MachineRef    `json:"target"`
	Reason         string               `json:"reason"`
	IdempotencyKey string               `json:"idempotency_key"`
	Parameters     map[string]any       `json:"parameters"`
}

// SessionApprovalGrant is the public, non-authority-bearing issuance result.
type SessionApprovalGrant struct {
	ApprovalID string                 `json:"approval_id"`
	Deadline   time.Time              `json:"deadline"`
	ExpiresAt  time.Time              `json:"expires_at"`
	Operation  SessionApprovalSummary `json:"operation"`
}

// IssueSessionMutationApproval issues or idempotently returns one immutable approval for agent:mcp-local.
func (s *SessionService) IssueSessionMutationApproval(ctx context.Context, params SessionApprovalIssueParams) (*SessionApprovalGrant, *domain.Receipt, error) {
	if err := s.validateSessionApprovalIssue(params); err != nil {
		return nil, nil, err
	}

	s.issueMu.Lock()
	defer s.issueMu.Unlock()

	existing, err := s.loadIssuedSessionApproval(ctx, params.IdempotencyKey)
	if err != nil {
		return nil, nil, err
	}
	approvedOp, issued, err := s.prepareIssuedSessionApproval(ctx, params, existing)
	if err != nil {
		return nil, nil, err
	}

	issuanceOp, issuanceReceipt, err := buildSessionApprovalIssuanceEvidence(params.Caller, approvedOp, issued)
	if err != nil {
		return nil, nil, err
	}
	if err := s.persistIssuedSessionApproval(ctx, existing, issued, issuanceOp, issuanceReceipt); err != nil {
		return nil, &issuanceReceipt, err
	}

	return sessionApprovalGrant(issued, approvedOp), &issuanceReceipt, nil
}

func (s *SessionService) validateSessionApprovalIssue(params SessionApprovalIssueParams) error {
	if err := validateSessionApprovalIssuer(params.Caller); err != nil {
		return err
	}
	if params.ValidFor < minSessionApprovalValidity || params.ValidFor > maxSessionApprovalValidity {
		return fmt.Errorf("session approval validity must be between %s and %s", minSessionApprovalValidity, maxSessionApprovalValidity)
	}
	if s.approvalStore == nil || s.auditStore == nil || s.receiptStore == nil {
		return errors.New("app: session approval persistence is unavailable")
	}
	return nil
}

func (s *SessionService) loadIssuedSessionApproval(ctx context.Context, idempotencyKey string) (*domain.Approval, error) {
	loaded, err := s.approvalStore.LoadIssuedContext(ctx, deriveSessionApprovalID(idempotencyKey))
	if errors.Is(err, approval.ErrApprovalNotIssued) {
		return nil, nil
	}
	return loaded, err
}

func (s *SessionService) prepareIssuedSessionApproval(ctx context.Context, params SessionApprovalIssueParams, existing *domain.Approval) (domain.Operation, domain.Approval, error) {
	issuedAt, deadline := s.sessionApprovalTimes(params.ValidFor, existing)
	approvedOp, err := s.buildIssuedSessionOperation(params, deadline)
	if err != nil {
		return domain.Operation{}, domain.Approval{}, err
	}
	approvedOp.Classification = s.resolveSafety(ctx, approvedOp.Target).Classification
	if !approvedOp.Classification.RequiresApproval() {
		return domain.Operation{}, domain.Approval{}, fmt.Errorf("%w: exact session operation does not currently require privileged approval", domain.ErrSessionAccessDenied)
	}
	approvedFingerprint, _, err := s.validateAndFingerprint(approvedOp)
	if err != nil {
		return domain.Operation{}, domain.Approval{}, err
	}
	issued := domain.Approval{
		ID: domain.ApprovalID(deriveSessionApprovalID(params.IdempotencyKey)), Actor: sessionApprovalBeneficiary, Target: approvedOp.Target,
		AuthorizedClass: approvedOp.Classification, Fingerprint: approvedFingerprint,
		IdempotencyKey: approvedOp.IdempotencyKey, IssuedAt: issuedAt, ExpiresAt: deadline,
	}
	if existing != nil && !equalIssuedSessionApproval(*existing, issued) {
		return domain.Operation{}, domain.Approval{}, receipt.ErrIdempotencyCollision
	}
	return approvedOp, issued, nil
}

func (s *SessionService) sessionApprovalTimes(validFor time.Duration, existing *domain.Approval) (time.Time, time.Time) {
	if existing != nil {
		return existing.IssuedAt, existing.ExpiresAt
	}
	issuedAt := s.now()
	return issuedAt, issuedAt.Add(validFor)
}

func (s *SessionService) persistIssuedSessionApproval(ctx context.Context, existing *domain.Approval, issued domain.Approval, issuanceOp domain.Operation, issuanceReceipt domain.Receipt) error {
	if existing == nil {
		if err := s.auditStore.RecordAdmissionIntentContext(ctx, issuanceOp); err != nil {
			return err
		}
		if err := s.approvalStore.IssueContext(ctx, issued); err != nil {
			return err
		}
	}
	return s.persistTerminalOutcomeContext(ctx, issuanceReceipt)
}

func validateSessionApprovalIssuer(caller domain.ActorContext) error {
	if err := caller.Validate(); err != nil {
		return err
	}
	if caller.IsDelegated() || !caller.HasScope(domain.ScopeSessionAdmin) || caller.AuthenticatedCaller == sessionApprovalBeneficiary {
		return domain.ErrSessionAccessDenied
	}
	return nil
}

func deriveSessionApprovalID(idempotencyKey string) string {
	digest := sha256.Sum256([]byte(string(sessionApprovalBeneficiary) + "\x00" + idempotencyKey))
	return "app-session-" + hex.EncodeToString(digest[:16])
}

func (s *SessionService) buildIssuedSessionOperation(params SessionApprovalIssueParams, deadline time.Time) (domain.Operation, error) {
	agent, err := sessionApprovalAgentActor()
	if err != nil {
		return domain.Operation{}, err
	}
	switch params.Kind {
	case "session.open":
		if err := domain.ValidateMachineGUID(params.Target); err != nil {
			return domain.Operation{}, err
		}
		op, _, _, _ := buildOpenOperation(SessionOpenParams{
			Target: params.Target, Caller: agent, Reason: params.Reason, IdempotencyKey: params.IdempotencyKey,
			Deadline: deadline, Cols: params.Cols, Rows: params.Rows, Term: params.Term,
		}, deadline)
		return op, nil
	case "session.write":
		target, err := s.sessionMgr.MutationTarget(params.SessionID, agent)
		if err != nil {
			return domain.Operation{}, err
		}
		return buildWriteOperation(SessionWriteParams{
			SessionID: params.SessionID, Caller: agent, Data: params.Data, Reason: params.Reason,
			IdempotencyKey: params.IdempotencyKey, Deadline: deadline,
		}, target, deadline), nil
	case "session.control":
		target, err := s.sessionMgr.MutationTarget(params.SessionID, agent)
		if err != nil {
			return domain.Operation{}, err
		}
		key, err := domain.NormalizeControlKey(string(params.Key))
		if err != nil {
			return domain.Operation{}, err
		}
		return buildControlOperation(SessionControlParams{
			SessionID: params.SessionID, Caller: agent, Key: key, Reason: params.Reason,
			IdempotencyKey: params.IdempotencyKey, Deadline: deadline,
		}, target, deadline), nil
	case "session.close":
		target, err := s.sessionMgr.MutationTarget(params.SessionID, agent)
		if err != nil {
			return domain.Operation{}, err
		}
		return buildCloseOperation(SessionCloseParams{
			SessionID: params.SessionID, Caller: agent, Reason: params.Reason,
			IdempotencyKey: params.IdempotencyKey, Deadline: deadline, Force: params.Force,
		}, target, deadline), nil
	default:
		return domain.Operation{}, fmt.Errorf("%w: unsupported session approval kind %q", domain.ErrInvalidOperationKind, params.Kind)
	}
}

func sessionApprovalAgentActor() (domain.ActorContext, error) {
	scopes := domain.NewScopeSet(
		domain.ScopeSessionOpen,
		domain.ScopeSessionRead,
		domain.ScopeSessionWrite,
		domain.ScopeSessionClose,
	)
	return domain.NewActorContext(sessionApprovalBeneficiary, sessionApprovalBeneficiary, scopes, scopes)
}

func equalIssuedSessionApproval(left, right domain.Approval) bool {
	return left.ID == right.ID && left.Actor == right.Actor && left.Target == right.Target &&
		left.AuthorizedClass == right.AuthorizedClass && left.Fingerprint == right.Fingerprint &&
		left.IdempotencyKey == right.IdempotencyKey && left.IssuedAt.Equal(right.IssuedAt) &&
		left.ExpiresAt.Equal(right.ExpiresAt) && !left.Consumed && left.ConsumedAt == nil
}

func buildSessionApprovalIssuanceEvidence(caller domain.ActorContext, approved domain.Operation, issued domain.Approval) (domain.Operation, domain.Receipt, error) {
	deadlineText := issued.ExpiresAt.UTC().Format(time.RFC3339Nano)
	op := domain.Operation{
		Kind: "session.approval.issue", Target: approved.Target, Actor: caller,
		Reason: approved.Reason, Deadline: issued.ExpiresAt,
		IdempotencyKey:     domain.DeriveApprovalIdempotencyKey("session-approval-issue:" + approved.IdempotencyKey),
		RequiredCapability: "session.approval.issue", RequiredScopes: []string{domain.ScopeSessionAdmin},
		Classification: domain.ClassDestructivePrivileged, EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters: map[string]any{
			"approval_id": string(issued.ID), "approved_fingerprint": string(issued.Fingerprint),
			"approved_kind": string(approved.Kind), "beneficiary": string(issued.Actor), "deadline": deadlineText,
		},
	}
	if err := op.Validate(); err != nil {
		return domain.Operation{}, domain.Receipt{}, err
	}
	if err := domain.ValidateOperationParameters(op.Kind, op.Parameters); err != nil {
		return domain.Operation{}, domain.Receipt{}, err
	}
	fp, err := op.Fingerprint()
	if err != nil {
		return domain.Operation{}, domain.Receipt{}, err
	}
	idFp, err := domain.ComputeIdempotencyFingerprint(op)
	if err != nil {
		return domain.Operation{}, domain.Receipt{}, err
	}
	receiptDigest := sha256.Sum256([]byte("session-approval-receipt\x00" + string(issued.ID)))
	rcpt := domain.Receipt{
		ReceiptID:     domain.ReceiptID("rcpt-" + hex.EncodeToString(receiptDigest[:16])),
		OperationKind: op.Kind, Fingerprint: fp, IdempotencyFingerprint: idFp,
		IdempotencyKey: op.IdempotencyKey, Actor: op.Actor.EffectiveActor, Target: op.Target,
		Class: op.Classification, EffectiveBackend: "amcd", StartedAt: issued.IssuedAt, CompletedAt: issued.IssuedAt,
		Outcome:         domain.ExecutionOutcome{Status: domain.OutcomeSuccess, ExitCode: 0},
		ObservationType: domain.ObservationObserved, EvidenceRefs: []string{string(issued.ID)},
		RedactionStatus: domain.RedactionApplied,
	}
	if err := rcpt.Validate(); err != nil {
		return domain.Operation{}, domain.Receipt{}, err
	}
	return op, rcpt, nil
}

func sessionApprovalGrant(issued domain.Approval, approved domain.Operation) *SessionApprovalGrant {
	return &SessionApprovalGrant{
		ApprovalID: string(issued.ID), Deadline: approved.Deadline.UTC(), ExpiresAt: issued.ExpiresAt.UTC(),
		Operation: SessionApprovalSummary{
			Kind: approved.Kind, Target: approved.Target, Reason: approved.Reason,
			IdempotencyKey: approved.IdempotencyKey, Parameters: domain.DeepCloneMap(approved.Parameters),
		},
	}
}
