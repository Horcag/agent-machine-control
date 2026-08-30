package app_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
)

func newMCPApprovalReferenceHarness(t *testing.T) *dynamicClassRetryHarness {
	t.Helper()
	h := newDynamicClassRetryHarness(t, destructiveSafetyResolution())
	scopes := domain.NewScopeSet(
		domain.ScopeSessionOpen,
		domain.ScopeSessionRead,
		domain.ScopeSessionWrite,
		domain.ScopeSessionClose,
	)
	actor, err := domain.NewActorContext("agent:mcp-approval-reference", "agent:mcp-approval-reference", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	h.actor = actor
	return h
}

func issueReferencedOpenApproval(
	t *testing.T,
	h *dynamicClassRetryHarness,
	params app.SessionOpenParams,
	id domain.ApprovalID,
	issuedAt, expiresAt time.Time,
) domain.Approval {
	t.Helper()
	op := domain.Operation{
		Kind: "session.open", Target: domain.MachineRef(params.Target), Actor: params.Caller,
		Reason: params.Reason, Deadline: h.now.Add(params.Timeout), IdempotencyKey: params.IdempotencyKey,
		RequiredCapability: domain.CapabilitySessionOpen, RequiredScopes: []string{domain.ScopeSessionOpen},
		Classification: domain.ClassDestructivePrivileged, EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters: map[string]any{
			"cols": params.Cols,
			"rows": params.Rows,
			"term": domain.DefaultTermType,
		},
	}
	fingerprint, err := op.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	issued := domain.Approval{
		ID: id, Actor: params.Caller.EffectiveActor, Target: domain.MachineRef(params.Target),
		AuthorizedClass: domain.ClassDestructivePrivileged, Fingerprint: fingerprint,
		IdempotencyKey: params.IdempotencyKey, IssuedAt: issuedAt, ExpiresAt: expiresAt,
	}
	if err := h.approvals.Issue(issued); err != nil {
		t.Fatal(err)
	}
	return issued
}

func TestSessionApprovalReferenceAllowsMCPActorAndExactRetryDoesNotReplay(t *testing.T) {
	h := newMCPApprovalReferenceHarness(t)
	params := h.openParams("idem-mcp-approval-reference")
	issued := issueReferencedOpenApproval(t, h, params, "app-mcp-reference-success", h.now.Add(-time.Minute), h.now.Add(time.Hour))
	params.ApprovalID = string(issued.ID)

	opened, firstReceipt, err := h.svc.OpenSession(context.Background(), params)
	if err != nil || opened == nil || firstReceipt == nil || firstReceipt.Outcome.Status != domain.OutcomeSuccess {
		t.Fatalf("referenced approval open = session %+v receipt %+v err %v", opened, firstReceipt, err)
	}
	if consumed, err := h.approvals.IsConsumed(string(issued.ID)); err != nil || !consumed {
		t.Fatalf("referenced approval consumed=%v err=%v", consumed, err)
	}

	retried, retryReceipt, err := h.svc.OpenSession(context.Background(), params)
	if err != nil || retried == nil || retryReceipt == nil || retryReceipt.ReceiptID != firstReceipt.ReceiptID || retried.ID != opened.ID {
		t.Fatalf("exact retry = session %+v receipt %+v err %v", retried, retryReceipt, err)
	}
	assertTransportEffects(t, h.transport, 1)
}

func TestSessionApprovalReferenceFailuresPerformNoSessionEffect(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *dynamicClassRetryHarness, *app.SessionOpenParams)
		check func(error) bool
	}{
		{
			name: "invalid ID",
			setup: func(_ *testing.T, _ *dynamicClassRetryHarness, params *app.SessionOpenParams) {
				params.ApprovalID = "../outside"
			},
			check: func(err error) bool { return errors.Is(err, domain.ErrInvalidApprovalRecord) },
		},
		{
			name: "missing provenance",
			setup: func(_ *testing.T, _ *dynamicClassRetryHarness, params *app.SessionOpenParams) {
				params.ApprovalID = "app-mcp-reference-missing"
			},
			check: func(err error) bool {
				var denied *app.PolicyDeniedError
				return errors.As(err, &denied) && denied.Reason == policy.DenialApprovalMismatch
			},
		},
		{
			name: "mismatched parameters",
			setup: func(t *testing.T, h *dynamicClassRetryHarness, params *app.SessionOpenParams) {
				issued := issueReferencedOpenApproval(t, h, *params, "app-mcp-reference-mismatch", h.now.Add(-time.Minute), h.now.Add(time.Hour))
				params.ApprovalID = string(issued.ID)
				params.Cols = 120
			},
			check: func(err error) bool {
				var denied *app.PolicyDeniedError
				return errors.As(err, &denied) && denied.Reason == policy.DenialApprovalMismatch
			},
		},
		{
			name: "expired",
			setup: func(t *testing.T, h *dynamicClassRetryHarness, params *app.SessionOpenParams) {
				issued := issueReferencedOpenApproval(t, h, *params, "app-mcp-reference-expired", h.now.Add(-time.Hour), h.now.Add(-time.Minute))
				params.ApprovalID = string(issued.ID)
			},
			check: func(err error) bool {
				var denied *app.PolicyDeniedError
				return errors.As(err, &denied) && denied.Reason == policy.DenialApprovalExpired
			},
		},
		{
			name: "consumed without durable retry truth",
			setup: func(t *testing.T, h *dynamicClassRetryHarness, params *app.SessionOpenParams) {
				issued := issueReferencedOpenApproval(t, h, *params, "app-mcp-reference-consumed", h.now.Add(-time.Minute), h.now.Add(time.Hour))
				if err := h.approvals.MarkConsumed(issued, h.now.Add(-time.Second)); err != nil {
					t.Fatal(err)
				}
				params.ApprovalID = string(issued.ID)
			},
			check: func(err error) bool { return errors.Is(err, domain.ErrApprovalConsumed) },
		},
		{
			name: "raw agent self approval",
			setup: func(t *testing.T, h *dynamicClassRetryHarness, params *app.SessionOpenParams) {
				issued := issueReferencedOpenApproval(t, h, *params, "app-mcp-reference-raw", h.now.Add(-time.Minute), h.now.Add(time.Hour))
				params.Approval = &issued
			},
			check: func(err error) bool {
				var denied *app.PolicyDeniedError
				return errors.As(err, &denied)
			},
		},
		{
			name: "raw and reference are mutually exclusive",
			setup: func(t *testing.T, h *dynamicClassRetryHarness, params *app.SessionOpenParams) {
				issued := issueReferencedOpenApproval(t, h, *params, "app-mcp-reference-exclusive", h.now.Add(-time.Minute), h.now.Add(time.Hour))
				params.Approval = &issued
				params.ApprovalID = string(issued.ID)
			},
			check: func(err error) bool { return errors.Is(err, domain.ErrInvalidApprovalRecord) },
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newMCPApprovalReferenceHarness(t)
			params := h.openParams(fmt.Sprintf("idem-reference-failure-%d", i))
			tt.setup(t, h, &params)
			opened, _, err := h.svc.OpenSession(context.Background(), params)
			if opened != nil || !tt.check(err) {
				t.Fatalf("open = session %+v err %v", opened, err)
			}
			assertTransportEffects(t, h.transport, 0)
		})
	}
}
