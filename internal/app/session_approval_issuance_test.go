package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func sessionApprovalOperator(t *testing.T) domain.ActorContext {
	t.Helper()
	scopes := domain.NewScopeSet(domain.ScopeSessionAdmin)
	actor, err := domain.NewActorContext("operator:approval-test", "operator:approval-test", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	return actor
}

func TestSessionApprovalIssuanceAuthorizesExactAgentOpenAndRetriesImmutably(t *testing.T) {
	h := newMCPApprovalReferenceHarness(t)
	issue := app.SessionApprovalIssueParams{
		Kind: "session.open", Caller: sessionApprovalOperator(t), Target: h.target,
		Reason: "approve exact agent session", IdempotencyKey: "idem-session-approval-issue",
		ValidFor: 45 * time.Second, Cols: 100, Rows: 30, Term: "xterm-256color",
	}

	grant, issueReceipt := requireIssuedSessionApproval(t, h, issue)
	if grant.Deadline != h.now.Add(45*time.Second) || grant.ExpiresAt.After(grant.Deadline) {
		t.Fatalf("grant deadline/expiry = %+v", grant)
	}
	requireSameSessionApprovalRetry(t, h, issue, grant, issueReceipt)
	requireApprovedAgentOpen(t, h, issue, grant)
	assertTransportEffects(t, h.transport, 1)
}

func requireIssuedSessionApproval(t *testing.T, h *dynamicClassRetryHarness, issue app.SessionApprovalIssueParams) (*app.SessionApprovalGrant, *domain.Receipt) {
	t.Helper()
	grant, issueReceipt, err := h.svc.IssueSessionMutationApproval(context.Background(), issue)
	if err != nil || grant == nil || issueReceipt == nil || issueReceipt.Outcome.Status != domain.OutcomeSuccess {
		t.Fatalf("issue approval = grant %+v receipt %+v err %v", grant, issueReceipt, err)
	}
	return grant, issueReceipt
}

func requireSameSessionApprovalRetry(t *testing.T, h *dynamicClassRetryHarness, issue app.SessionApprovalIssueParams, grant *app.SessionApprovalGrant, issueReceipt *domain.Receipt) {
	t.Helper()
	retried, retryReceipt, err := h.svc.IssueSessionMutationApproval(context.Background(), issue)
	if err != nil || retried == nil || retried.ApprovalID != grant.ApprovalID || retried.Deadline != grant.Deadline || retryReceipt == nil || retryReceipt.ReceiptID != issueReceipt.ReceiptID {
		t.Fatalf("retry issue = grant %+v receipt %+v err %v", retried, retryReceipt, err)
	}
}

func requireApprovedAgentOpen(t *testing.T, h *dynamicClassRetryHarness, issue app.SessionApprovalIssueParams, grant *app.SessionApprovalGrant) {
	t.Helper()
	opened, mutationReceipt, err := h.svc.OpenSession(context.Background(), app.SessionOpenParams{
		Target: h.target, Caller: h.actor, Reason: issue.Reason, IdempotencyKey: issue.IdempotencyKey,
		Deadline: grant.Deadline, ApprovalID: grant.ApprovalID, Cols: issue.Cols, Rows: issue.Rows, Term: issue.Term,
	})
	if err != nil || opened == nil || mutationReceipt == nil || mutationReceipt.Outcome.Status != domain.OutcomeSuccess {
		t.Fatalf("approved open = session %+v receipt %+v err %v", opened, mutationReceipt, err)
	}
}

func TestSessionApprovalIssuanceRejectsAgentAndChangedOperationIdentity(t *testing.T) {
	h := newMCPApprovalReferenceHarness(t)
	base := app.SessionApprovalIssueParams{
		Kind: "session.open", Caller: sessionApprovalOperator(t), Target: h.target,
		Reason: "approve stable operation", IdempotencyKey: "idem-session-approval-conflict",
		ValidFor: 30 * time.Second, Cols: 80, Rows: 24, Term: domain.DefaultTermType,
	}
	if _, _, err := h.svc.IssueSessionMutationApproval(context.Background(), base); err != nil {
		t.Fatalf("initial issuance: %v", err)
	}

	changed := base
	changed.Reason = "changed operation"
	if _, _, err := h.svc.IssueSessionMutationApproval(context.Background(), changed); !errors.Is(err, receipt.ErrIdempotencyCollision) {
		t.Fatalf("changed operation error = %v, want idempotency collision", err)
	}

	selfIssue := base
	selfIssue.Caller = h.actor
	selfIssue.IdempotencyKey = "idem-session-agent-self-issue"
	if _, _, err := h.svc.IssueSessionMutationApproval(context.Background(), selfIssue); !errors.Is(err, domain.ErrSessionAccessDenied) {
		t.Fatalf("agent self issuance error = %v", err)
	}
	assertTransportEffects(t, h.transport, 0)
}

func TestSessionApprovalIssuanceRequiresCurrentPrivilegedClassification(t *testing.T) {
	h := newDynamicClassRetryHarness(t, reversibleSafetyResolution())
	_, _, err := h.svc.IssueSessionMutationApproval(context.Background(), app.SessionApprovalIssueParams{
		Kind: "session.open", Caller: sessionApprovalOperator(t), Target: h.target,
		Reason: "must not preapprove reversible operation", IdempotencyKey: "idem-session-reversible-issue",
		ValidFor: 30 * time.Second, Cols: 80, Rows: 24, Term: domain.DefaultTermType,
	})
	if err == nil {
		t.Fatal("expected reversible operation issuance denial")
	}
	assertTransportEffects(t, h.transport, 0)
}

func TestSessionApprovalIssuanceRejectsInvalidAuthorityValidityAndOperation(t *testing.T) {
	h := newMCPApprovalReferenceHarness(t)
	base := app.SessionApprovalIssueParams{
		Kind: "session.open", Caller: sessionApprovalOperator(t), Target: h.target,
		Reason: "validate approval issuance", IdempotencyKey: "idem-session-issue-validation",
		ValidFor: time.Minute, Cols: 80, Rows: 24, Term: domain.DefaultTermType,
	}

	tooShort := base
	tooShort.ValidFor = time.Millisecond
	if _, _, err := h.svc.IssueSessionMutationApproval(context.Background(), tooShort); err == nil {
		t.Fatal("expected short validity rejection")
	}
	unsupported := base
	unsupported.Kind = "session.read"
	unsupported.IdempotencyKey = "idem-session-issue-unsupported"
	if _, _, err := h.svc.IssueSessionMutationApproval(context.Background(), unsupported); !errors.Is(err, domain.ErrInvalidOperationKind) {
		t.Fatalf("unsupported operation error=%v", err)
	}
	invalidTarget := base
	invalidTarget.Target = "not-a-guid"
	invalidTarget.IdempotencyKey = "idem-session-issue-invalid-target"
	if _, _, err := h.svc.IssueSessionMutationApproval(context.Background(), invalidTarget); err == nil {
		t.Fatal("expected invalid target rejection")
	}

	scopes := domain.NewScopeSet(domain.ScopeSessionAdmin)
	delegated, err := domain.NewActorContext("operator:delegating", "agent:delegate", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	delegatedIssue := base
	delegatedIssue.Caller = delegated
	delegatedIssue.IdempotencyKey = "idem-session-issue-delegated"
	if _, _, err := h.svc.IssueSessionMutationApproval(context.Background(), delegatedIssue); !errors.Is(err, domain.ErrSessionAccessDenied) {
		t.Fatalf("delegated issuance error=%v", err)
	}
}

func TestSessionApprovalIssuanceFailsClosedWithoutPersistenceOwners(t *testing.T) {
	service := app.NewSessionService(nil, nil, nil, nil, nil, nil)
	_, _, err := service.IssueSessionMutationApproval(context.Background(), app.SessionApprovalIssueParams{
		Kind: "session.open", Caller: sessionApprovalOperator(t), Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Reason: "missing persistence owners", IdempotencyKey: "idem-session-issue-no-stores", ValidFor: time.Minute,
	})
	if err == nil {
		t.Fatal("expected unavailable persistence rejection")
	}
}

func openIssuedAgentSession(t *testing.T, h *dynamicClassRetryHarness, key string) domain.SessionID {
	t.Helper()
	issue := app.SessionApprovalIssueParams{
		Kind: "session.open", Caller: sessionApprovalOperator(t), Target: h.target,
		Reason: "create agent-owned session", IdempotencyKey: key, ValidFor: time.Minute,
		Cols: 80, Rows: 24, Term: domain.DefaultTermType,
	}
	grant, _, err := h.svc.IssueSessionMutationApproval(context.Background(), issue)
	if err != nil {
		t.Fatal(err)
	}
	opened, _, err := h.svc.OpenSession(context.Background(), app.SessionOpenParams{
		Target: h.target, Caller: h.actor, Reason: issue.Reason, IdempotencyKey: issue.IdempotencyKey,
		Deadline: grant.Deadline, ApprovalID: grant.ApprovalID, Cols: 80, Rows: 24, Term: domain.DefaultTermType,
	})
	if err != nil {
		t.Fatal(err)
	}
	return opened.ID
}

func TestSessionApprovalIssuanceRejectsChangedFieldsForSameMutationIdentity(t *testing.T) {
	h := newMCPApprovalReferenceHarness(t)
	firstSession := openIssuedAgentSession(t, h, "idem-create-agent-session-one")
	secondSession := openIssuedAgentSession(t, h, "idem-create-agent-session-two")
	operator := sessionApprovalOperator(t)

	tests := []struct {
		name   string
		base   app.SessionApprovalIssueParams
		mutate func(*app.SessionApprovalIssueParams)
	}{
		{
			name: "open dimensions", base: app.SessionApprovalIssueParams{
				Kind: "session.open", Caller: operator, Target: h.target, Reason: "approve dimensions",
				IdempotencyKey: "idem-issue-open-dimensions", ValidFor: time.Minute, Cols: 80, Rows: 24, Term: domain.DefaultTermType,
			}, mutate: func(p *app.SessionApprovalIssueParams) { p.Cols = 120 },
		},
		{
			name: "open target", base: app.SessionApprovalIssueParams{
				Kind: "session.open", Caller: operator, Target: h.target, Reason: "approve target",
				IdempotencyKey: "idem-issue-open-target", ValidFor: time.Minute, Cols: 80, Rows: 24, Term: domain.DefaultTermType,
			}, mutate: func(p *app.SessionApprovalIssueParams) { p.Target = "d4a523d4-6b99-4d62-a5e2-4752c0f20001" },
		},
		{
			name: "write plaintext hash and length", base: app.SessionApprovalIssueParams{
				Kind: "session.write", Caller: operator, SessionID: firstSession, Data: "first secret",
				Reason: "approve write", IdempotencyKey: "idem-issue-write-data", ValidFor: time.Minute,
			}, mutate: func(p *app.SessionApprovalIssueParams) { p.Data = "changed secret payload" },
		},
		{
			name: "session identity", base: app.SessionApprovalIssueParams{
				Kind: "session.write", Caller: operator, SessionID: firstSession, Data: "stable payload",
				Reason: "approve session", IdempotencyKey: "idem-issue-write-session", ValidFor: time.Minute,
			}, mutate: func(p *app.SessionApprovalIssueParams) { p.SessionID = secondSession },
		},
		{
			name: "control key", base: app.SessionApprovalIssueParams{
				Kind: "session.control", Caller: operator, SessionID: firstSession, Key: domain.ControlKeyCtrlC,
				Reason: "approve control", IdempotencyKey: "idem-issue-control-key", ValidFor: time.Minute,
			}, mutate: func(p *app.SessionApprovalIssueParams) { p.Key = domain.ControlKeyEnter },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := h.svc.IssueSessionMutationApproval(context.Background(), test.base); err != nil {
				t.Fatalf("baseline issuance: %v", err)
			}
			changed := test.base
			test.mutate(&changed)
			if _, _, err := h.svc.IssueSessionMutationApproval(context.Background(), changed); !errors.Is(err, receipt.ErrIdempotencyCollision) {
				t.Fatalf("changed issuance error=%v, want idempotency collision", err)
			}
		})
	}
}
