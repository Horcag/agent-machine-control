package app

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestGuestApprovalExactBeneficiaryAndPayload(t *testing.T) {
	h := newGuestHarness(t)
	h.safety.destructive = true
	operatorScopes := domain.NewScopeSet(domain.ScopeSessionAdmin)
	issuer, err := domain.NewActorContext("fixture-operator", "fixture-operator", operatorScopes, operatorScopes)
	if err != nil {
		t.Fatal(err)
	}
	prepare := h.params("prepare", "approved-prepare")
	prepare.Caller = issuer
	grant, issuance, err := h.service.IssueApproval(context.Background(), prepare, time.Minute)
	if err != nil || issuance == nil || grant == nil {
		t.Fatalf("issue %+v %v", grant, err)
	}
	agent, err := sessionApprovalAgentActor()
	if err != nil {
		t.Fatal(err)
	}
	agent.CallerPermissions[domain.ScopeEvidenceCapture] = struct{}{}
	agent.EffectivePermissions[domain.ScopeEvidenceCapture] = struct{}{}
	prepare.Caller = agent
	prepare.Deadline = grant.Deadline
	prepare.ApprovalID = grant.ApprovalID
	out, err := h.service.Execute(context.Background(), prepare, nil, nil)
	if err != nil || out.Receipt == nil {
		t.Fatalf("prepare %+v %v", out, err)
	}
	command := h.params("exec", "approved-command")
	command.Caller = issuer
	command.Size = 1
	command.SHA256 = strings.Repeat("a", 64)
	command.MaxOutput = 10
	commandGrant, _, err := h.service.IssueApproval(context.Background(), command, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	command.Caller = agent
	command.ApprovalID = commandGrant.ApprovalID
	command.Deadline = commandGrant.Deadline
	command.Size = 2
	_, err = h.service.Execute(context.Background(), command, strings.NewReader("xx"), io.Discard)
	if err == nil || h.transport.executed != 1 {
		t.Fatalf("changed payload approved: %v", err)
	}
}
func TestGuestAgentCannotIssueApproval(t *testing.T) {
	h := newGuestHarness(t)
	h.safety.destructive = true
	p := h.params("prepare", "agent-issuance")
	p.Caller.AuthenticatedCaller = sessionApprovalBeneficiary
	p.Caller.EffectiveActor = sessionApprovalBeneficiary
	p.Caller.CallerPermissions[domain.ScopeSessionAdmin] = struct{}{}
	p.Caller.EffectivePermissions[domain.ScopeSessionAdmin] = struct{}{}
	if _, _, err := h.service.IssueApproval(context.Background(), p, time.Minute); err == nil {
		t.Fatal("agent issued operator approval")
	}
}
