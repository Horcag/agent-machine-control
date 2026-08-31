package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/sessions"
	"github.com/Horcag/agent-machine-control/internal/target"
)

type fixedSessionTargetResolver struct {
	mu         sync.Mutex
	resolution app.TargetResolution
	calls      []string
	err        error
}

func (r *fixedSessionTargetResolver) ResolveTarget(_ context.Context, reference string) (app.TargetResolution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, reference)
	if r.err != nil {
		return app.TargetResolution{}, r.err
	}
	return r.resolution, nil
}

func (r *fixedSessionTargetResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *fixedSessionTargetResolver) setResolution(resolution app.TargetResolution) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resolution = resolution
}

func TestSessionOpenCanonicalizesEquivalentReferencesBeforeAnyEffect(t *testing.T) {
	h := newDynamicClassRetryHarness(t, reversibleSafetyResolution())
	locator, err := domain.NewMachineLocator(domain.LocalHostID, h.target)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &fixedSessionTargetResolver{resolution: app.TargetResolution{Locator: locator, ProviderVMID: h.target}}
	h.svc = app.NewSessionService(h.manager, h.safety, nil, h.audits, h.receipts, h.approvals,
		app.WithSessionClock(func() time.Time { return h.now }), app.WithSessionTargetResolver(resolver))

	references := []string{"", "default", "primary", h.target, locator.String()}
	type openResult struct {
		reference string
		obs       *domain.SessionObservation
		receipt   *domain.Receipt
		err       error
	}
	results := make(chan openResult, len(references))
	var group sync.WaitGroup
	for _, reference := range references {
		group.Go(func() {
			obs, rcpt, openErr := h.svc.OpenSession(context.Background(), app.SessionOpenParams{
				Target: reference, Caller: h.actor, Reason: "open enrolled target", IdempotencyKey: "canonical-open-retry",
				Timeout: 30 * time.Second, Cols: 80, Rows: 24,
			})
			results <- openResult{reference: reference, obs: obs, receipt: rcpt, err: openErr}
		})
	}
	group.Wait()
	close(results)
	var sessionID domain.SessionID
	var receiptID domain.ReceiptID
	for result := range results {
		if result.err != nil || result.obs == nil || result.receipt == nil {
			t.Fatalf("OpenSession(%q) = obs=%+v receipt=%+v err=%v", result.reference, result.obs, result.receipt, result.err)
		}
		if result.obs.Target != domain.MachineRef(locator.String()) {
			t.Fatalf("OpenSession(%q) target = %q, want canonical %q", result.reference, result.obs.Target, locator)
		}
		if sessionID == "" {
			sessionID, receiptID = result.obs.ID, result.receipt.ReceiptID
		} else if result.obs.ID != sessionID || result.receipt.ReceiptID != receiptID {
			t.Fatalf("OpenSession(%q) returned a distinct durable result", result.reference)
		}
	}
	if resolver.callCount() != len(references) {
		t.Fatalf("resolver call count = %d, want %d", resolver.callCount(), len(references))
	}
	assertTransportEffects(t, h.transport, 1)
	if h.transport.lastDialTarget != domain.MachineRef(h.target) {
		t.Fatalf("transport target = %q, want provider GUID %q", h.transport.lastDialTarget, h.target)
	}
}

func TestSessionOpenSharesPhysicalLeaseWithCanonicalLocator(t *testing.T) {
	h := newDynamicClassRetryHarness(t, reversibleSafetyResolution())
	locator, err := domain.NewMachineLocator(domain.LocalHostID, h.target)
	if err != nil {
		t.Fatal(err)
	}
	leaseDir := t.TempDir()
	leaseManager := lease.NewManager(leaseDir)
	h.svc = app.NewSessionService(h.manager, h.safety, leaseManager, h.audits, h.receipts, h.approvals,
		app.WithSessionClock(func() time.Time { return h.now }),
		app.WithSessionTargetResolver(&fixedSessionTargetResolver{
			resolution: app.TargetResolution{Locator: locator, ProviderVMID: h.target},
		}),
	)

	recoveryLease, err := leaseManager.Acquire(context.Background(), h.target, "machine.start", "recovery-fingerprint", time.Minute)
	if err != nil {
		t.Fatalf("acquire recovery lease: %v", err)
	}
	defer func() { _ = leaseManager.Release(context.Background(), recoveryLease) }()

	obs, rcpt, err := h.svc.OpenSession(context.Background(), app.SessionOpenParams{
		Target: "default", Caller: h.actor, Reason: "open while recovery owns the VM lease", IdempotencyKey: "locator-lease-conflict",
		Timeout: time.Minute, Cols: 80, Rows: 24,
	})
	if !errors.Is(err, lease.ErrLeaseConflict) || obs != nil || rcpt != nil {
		t.Fatalf("OpenSession under recovery lease = obs=%+v receipt=%+v err=%v", obs, rcpt, err)
	}
	assertTransportEffects(t, h.transport, 0)
	if _, err := os.Stat(filepath.Join(leaseDir, h.target+".lease.json")); err != nil {
		t.Fatalf("physical recovery lease disappeared: %v", err)
	}
	if _, err := os.Stat(filepath.Join(leaseDir, locator.String()+".lease.json")); !os.IsNotExist(err) {
		t.Fatalf("locator-derived lease path exists or is unreadable: %v", err)
	}
}

func TestSessionOpenTargetResolutionFailuresPrecedeApprovalJournalAndTransport(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "missing", err: target.ErrNoDefault},
		{name: "different", err: target.ErrDifferentTarget},
		{name: "cleared", err: target.ErrNoDefault},
		{name: "stale", err: domain.ErrMachineReferenceStale},
		{name: "unavailable", err: domain.ErrMachineHostUnavailable},
		{name: "denied", err: domain.ErrMachineAccessDenied},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newDynamicClassRetryHarness(t, destructiveSafetyResolution())
			resolver := &fixedSessionTargetResolver{err: test.err}
			h.svc = app.NewSessionService(h.manager, h.safety, nil, h.audits, h.receipts, h.approvals,
				app.WithSessionClock(func() time.Time { return h.now }), app.WithSessionTargetResolver(resolver))
			params := h.openParams("target-failure-" + test.name)
			issued := destructiveOpenApproval(t, h, params)
			params.Target = "default"
			params.Approval = issued

			obs, rcpt, err := h.svc.OpenSession(context.Background(), params)
			if !errors.Is(err, test.err) || obs != nil || rcpt != nil {
				t.Fatalf("OpenSession failure = obs=%+v receipt=%+v err=%v", obs, rcpt, err)
			}
			if consumed, consumedErr := h.approvals.IsConsumed(string(issued.ID)); consumedErr != nil || consumed {
				t.Fatalf("approval consumed=%v err=%v", consumed, consumedErr)
			}
			records, journalErr := h.manager.MutationJournal().ListContext(context.Background())
			if journalErr != nil || len(records) != 0 {
				t.Fatalf("journal records=%+v err=%v", records, journalErr)
			}
			receipts, receiptErr := h.receipts.List(10, "")
			if receiptErr != nil || len(receipts) != 0 {
				t.Fatalf("receipts=%+v err=%v", receipts, receiptErr)
			}
			if got := h.safety.calls.Load(); got != 0 {
				t.Fatalf("safety resolutions=%d, want 0", got)
			}
			assertTransportEffects(t, h.transport, 0)
		})
	}
}

func TestSessionApprovalIssuanceCanonicalizesEquivalentOpenTargets(t *testing.T) {
	h := newMCPApprovalReferenceHarness(t)
	locator, err := domain.NewMachineLocator(domain.LocalHostID, h.target)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &fixedSessionTargetResolver{resolution: app.TargetResolution{Locator: locator, ProviderVMID: h.target}}
	h.svc = app.NewSessionService(h.manager, h.safety, nil, h.audits, h.receipts, h.approvals,
		app.WithSessionClock(func() time.Time { return h.now }), app.WithSessionTargetResolver(resolver))
	issue, grant := issueCanonicalOpenApproval(t, h, resolver, locator)
	assertCanonicalIssuedApproval(t, h, locator, grant)
	openWithCanonicalIssuedApproval(t, h, locator, issue, grant)
	assertTransportEffects(t, h.transport, 1)
	if resolver.callCount() != 3 {
		t.Fatalf("resolver call count after open = %d", resolver.callCount())
	}
}

func issueCanonicalOpenApproval(
	t *testing.T,
	h *dynamicClassRetryHarness,
	resolver *fixedSessionTargetResolver,
	locator domain.MachineLocator,
) (app.SessionApprovalIssueParams, *app.SessionApprovalGrant) {
	t.Helper()
	issue := app.SessionApprovalIssueParams{
		Kind: "session.open", Caller: sessionApprovalOperator(t), Target: "default", Reason: "approve enrolled target",
		IdempotencyKey: "canonical-approval", ValidFor: time.Minute, Cols: 80, Rows: 24, Term: domain.DefaultTermType,
	}
	grant, _, err := h.svc.IssueSessionMutationApproval(context.Background(), issue)
	if err != nil || grant == nil || grant.Operation.Target != domain.MachineRef(locator.String()) {
		t.Fatalf("default issuance = grant=%+v err=%v", grant, err)
	}
	issue.Target = h.target
	retried, _, err := h.svc.IssueSessionMutationApproval(context.Background(), issue)
	if err != nil || retried == nil || retried.ApprovalID != grant.ApprovalID || retried.Operation.Target != grant.Operation.Target {
		t.Fatalf("equivalent issuance retry = grant=%+v err=%v", retried, err)
	}
	if resolver.callCount() != 2 {
		t.Fatalf("resolver call count = %d", resolver.callCount())
	}
	return issue, grant
}

func assertCanonicalIssuedApproval(t *testing.T, h *dynamicClassRetryHarness, locator domain.MachineLocator, grant *app.SessionApprovalGrant) {
	t.Helper()
	issued, err := h.approvals.LoadIssuedContext(context.Background(), grant.ApprovalID)
	if err != nil || issued.Actor != "agent:mcp-local" || issued.Target != domain.MachineRef(locator.String()) {
		t.Fatalf("issued agent binding = %+v err=%v", issued, err)
	}
}

func openWithCanonicalIssuedApproval(
	t *testing.T,
	h *dynamicClassRetryHarness,
	locator domain.MachineLocator,
	issue app.SessionApprovalIssueParams,
	grant *app.SessionApprovalGrant,
) {
	t.Helper()
	opened, rcpt, err := h.svc.OpenSession(context.Background(), app.SessionOpenParams{
		Target: h.target, Caller: h.actor, Reason: issue.Reason, IdempotencyKey: issue.IdempotencyKey,
		Deadline: grant.Deadline, ApprovalID: grant.ApprovalID, Cols: issue.Cols, Rows: issue.Rows, Term: issue.Term,
	})
	if err != nil || opened == nil || rcpt == nil || opened.Target != domain.MachineRef(locator.String()) {
		t.Fatalf("approved equivalent-reference open = session=%+v receipt=%+v err=%v", opened, rcpt, err)
	}
	if consumed, consumeErr := h.approvals.IsConsumed(grant.ApprovalID); consumeErr != nil || !consumed {
		t.Fatalf("agent approval consumed=%v err=%v", consumed, consumeErr)
	}
}

func TestSessionOpenSameIdempotencyKeyConflictsAcrossCanonicalTargets(t *testing.T) {
	h := newDynamicClassRetryHarness(t, reversibleSafetyResolution())
	firstLocator, err := domain.NewMachineLocator(domain.LocalHostID, h.target)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &fixedSessionTargetResolver{resolution: app.TargetResolution{Locator: firstLocator, ProviderVMID: h.target}}
	h.svc = app.NewSessionService(h.manager, h.safety, nil, h.audits, h.receipts, h.approvals,
		app.WithSessionClock(func() time.Time { return h.now }), app.WithSessionTargetResolver(resolver))
	params := h.openParams("canonical-target-conflict")
	if opened, _, openErr := h.svc.OpenSession(context.Background(), params); openErr != nil || opened == nil {
		t.Fatalf("first open = session=%+v err=%v", opened, openErr)
	}
	const otherVMID = "d4a523d4-6b99-4d62-a5e2-4752c0f20001"
	otherLocator, err := domain.NewMachineLocator(domain.LocalHostID, otherVMID)
	if err != nil {
		t.Fatal(err)
	}
	resolver.setResolution(app.TargetResolution{Locator: otherLocator, ProviderVMID: otherVMID})
	params.Target = "other"
	if opened, _, conflictErr := h.svc.OpenSession(context.Background(), params); opened != nil ||
		(!errors.Is(conflictErr, receipt.ErrIdempotencyCollision) && !errors.Is(conflictErr, sessions.ErrMutationReservationCollision)) {
		t.Fatalf("different canonical target = session=%+v err=%v", opened, conflictErr)
	}
	assertTransportEffects(t, h.transport, 1)
}
