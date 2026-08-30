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
	actor, err := domain.NewActorContext("agent:mcp-local", "agent:mcp-local", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	h.actor = actor
	return h
}

type approvalReferenceFailureCase struct {
	name    string
	setup   func(*testing.T, *dynamicClassRetryHarness, *app.SessionOpenParams)
	check   func(error) bool
	durable bool
}

func issueReferencedOpenApproval(
	t *testing.T,
	h *dynamicClassRetryHarness,
	params app.SessionOpenParams,
	id domain.ApprovalID,
	issuedAt, expiresAt time.Time,
) domain.Approval {
	t.Helper()
	deadline := h.now.Add(params.Timeout)
	if !params.Deadline.IsZero() {
		deadline = params.Deadline
	}
	op := domain.Operation{
		Kind: "session.open", Target: domain.MachineRef(params.Target), Actor: params.Caller,
		Reason: params.Reason, Deadline: deadline, IdempotencyKey: params.IdempotencyKey,
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
	params.Deadline = h.now.Add(30 * time.Second)
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

func TestSessionApprovalReferenceChangedDeadlineDenialIsDurableAndIdempotent(t *testing.T) {
	h := newMCPApprovalReferenceHarness(t)
	params := h.openParams("idem-mcp-approval-deadline-mismatch")
	params.Deadline = h.now.Add(30 * time.Second)
	issued := issueReferencedOpenApproval(t, h, params, "app-mcp-reference-deadline", h.now.Add(-time.Minute), h.now.Add(time.Hour))
	params.ApprovalID = string(issued.ID)
	params.Deadline = params.Deadline.Add(time.Nanosecond)

	opened, firstReceipt, firstErr := h.svc.OpenSession(context.Background(), params)
	if opened != nil || firstReceipt == nil || firstReceipt.Outcome.Status != domain.OutcomeDenied {
		t.Fatalf("deadline mismatch = session %+v receipt %+v err %v", opened, firstReceipt, firstErr)
	}
	var denied *app.PolicyDeniedError
	if !errors.As(firstErr, &denied) || denied.Reason != policy.DenialApprovalMismatch {
		t.Fatalf("deadline mismatch error = %v", firstErr)
	}

	opened, retryReceipt, retryErr := h.svc.OpenSession(context.Background(), params)
	if opened != nil || retryReceipt == nil || retryReceipt.ReceiptID != firstReceipt.ReceiptID {
		t.Fatalf("deadline mismatch retry = session %+v receipt %+v err %v", opened, retryReceipt, retryErr)
	}
	if !errors.As(retryErr, &denied) || denied.Reason != policy.DenialApprovalMismatch {
		t.Fatalf("deadline mismatch retry error = %v", retryErr)
	}
	assertTransportEffects(t, h.transport, 0)
}

func TestSessionApprovalReferenceMissingDenialIsDurableAndIdempotent(t *testing.T) {
	h := newMCPApprovalReferenceHarness(t)
	params := h.openParams("idem-mcp-approval-missing-durable")
	params.ApprovalID = "app-mcp-reference-missing-durable"
	params.Deadline = h.now.Add(30 * time.Second)

	opened, firstReceipt, firstErr := h.svc.OpenSession(context.Background(), params)
	if opened != nil || firstReceipt == nil || firstReceipt.Outcome.Status != domain.OutcomeDenied || firstReceipt.Class != domain.ClassDestructivePrivileged {
		t.Fatalf("missing reference = session %+v receipt %+v err %v", opened, firstReceipt, firstErr)
	}
	var denied *app.PolicyDeniedError
	if !errors.As(firstErr, &denied) || denied.Reason != policy.DenialApprovalMismatch {
		t.Fatalf("missing reference error = %v", firstErr)
	}

	opened, retryReceipt, retryErr := h.svc.OpenSession(context.Background(), params)
	if opened != nil || retryReceipt == nil || retryReceipt.ReceiptID != firstReceipt.ReceiptID {
		t.Fatalf("missing reference retry = session %+v receipt %+v err %v", opened, retryReceipt, retryErr)
	}
	if !errors.As(retryErr, &denied) || denied.Reason != policy.DenialApprovalMismatch {
		t.Fatalf("missing reference retry error = %v", retryErr)
	}
	assertTransportEffects(t, h.transport, 0)
}

func TestSessionApprovalReferencePastExactDeadlineDenialIsDurable(t *testing.T) {
	h := newMCPApprovalReferenceHarness(t)
	params := h.openParams("idem-mcp-approval-past-deadline")
	params.Deadline = h.now.Add(-time.Second)
	issued := issueReferencedOpenApproval(t, h, params, "app-mcp-reference-past-deadline", h.now.Add(-time.Minute), params.Deadline)
	params.ApprovalID = string(issued.ID)

	opened, firstReceipt, firstErr := h.svc.OpenSession(context.Background(), params)
	if opened != nil || firstReceipt == nil || firstReceipt.Outcome.Status != domain.OutcomeDenied {
		t.Fatalf("past deadline = session %+v receipt %+v err %v", opened, firstReceipt, firstErr)
	}
	var denied *app.PolicyDeniedError
	if !errors.As(firstErr, &denied) || denied.Reason != policy.DenialDeadlinePassed {
		t.Fatalf("past deadline error = %v", firstErr)
	}
	opened, retryReceipt, retryErr := h.svc.OpenSession(context.Background(), params)
	if opened != nil || retryReceipt == nil || retryReceipt.ReceiptID != firstReceipt.ReceiptID || !errors.As(retryErr, &denied) {
		t.Fatalf("past deadline retry = session %+v receipt %+v err %v", opened, retryReceipt, retryErr)
	}
	assertTransportEffects(t, h.transport, 0)
}

func TestSessionApprovalReferenceFailuresPerformNoSessionEffect(t *testing.T) {
	tests := []approvalReferenceFailureCase{
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
			durable: true,
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
			durable: true,
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
			durable: true,
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
			check: func(err error) bool {
				var denied *app.PolicyDeniedError
				return errors.Is(err, domain.ErrApprovalConsumed) || (errors.As(err, &denied) && denied.Reason == policy.DenialApprovalConsumed)
			},
			durable: true,
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
			durable: true,
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
			runApprovalReferenceFailureCase(t, i, tt)
		})
	}
}

func runApprovalReferenceFailureCase(t *testing.T, index int, test approvalReferenceFailureCase) {
	t.Helper()
	h := newMCPApprovalReferenceHarness(t)
	params := h.openParams(fmt.Sprintf("idem-reference-failure-%d", index))
	params.Deadline = h.now.Add(params.Timeout)
	test.setup(t, h, &params)
	opened, denialReceipt, err := h.svc.OpenSession(context.Background(), params)
	if opened != nil || !test.check(err) {
		t.Fatalf("open = session %+v err %v", opened, err)
	}
	if test.durable {
		requireDurableApprovalReferenceRetry(t, h, params, denialReceipt, test.check)
	}
	assertTransportEffects(t, h.transport, 0)
}

func requireDurableApprovalReferenceRetry(t *testing.T, h *dynamicClassRetryHarness, params app.SessionOpenParams, denialReceipt *domain.Receipt, check func(error) bool) {
	t.Helper()
	if denialReceipt == nil || denialReceipt.Outcome.Status != domain.OutcomeDenied {
		t.Fatalf("durable denial receipt = %+v", denialReceipt)
	}
	retried, retryReceipt, retryErr := h.svc.OpenSession(context.Background(), params)
	if retried != nil || retryReceipt == nil || retryReceipt.ReceiptID != denialReceipt.ReceiptID || !check(retryErr) {
		t.Fatalf("retry = session %+v receipt %+v err %v", retried, retryReceipt, retryErr)
	}
}
