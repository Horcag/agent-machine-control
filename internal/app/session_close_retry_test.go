package app_test

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/sessions"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

type retryCloseTransport struct{ channel *retryCloseChannel }

func (t retryCloseTransport) Dial(context.Context, domain.MachineRef, uint16, uint16, string) (guestssh.Channel, error) {
	return t.channel, nil
}

type destructiveAfterOpenSafety struct{ calls atomic.Int32 }

func (s *destructiveAfterOpenSafety) ResolveSafety(ctx context.Context, target domain.MachineRef) (app.SafetyResolution, error) {
	if s.calls.Add(1) == 1 {
		return diagnosticReversibleSafety{}.ResolveSafety(ctx, target)
	}
	return app.SafetyResolution{Classification: domain.ClassDestructivePrivileged}, nil
}

type retryCloseChannel struct {
	mu       sync.Mutex
	done     chan struct{}
	doneOnce sync.Once
	last     guestssh.CloseOutcome
	calls    atomic.Int32
}

func (c *retryCloseChannel) Read([]byte) (int, error)                          { <-c.done; return 0, io.EOF }
func (c *retryCloseChannel) Write(_ context.Context, data []byte) (int, error) { return len(data), nil }
func (c *retryCloseChannel) SendControl(context.Context, domain.ControlKey) (guestssh.ControlResult, error) {
	return guestssh.ControlResult{AcceptedBytes: 1, EffectApplied: true}, nil
}
func (c *retryCloseChannel) Resize(uint16, uint16) error { return nil }
func (c *retryCloseChannel) Wait() (int, error)          { <-c.done; return 0, nil }

func (c *retryCloseChannel) Close(_ context.Context) error {
	if c.calls.Add(1) == 1 {
		c.mu.Lock()
		c.last = guestssh.CloseOutcome{Complete: false, Err: context.DeadlineExceeded}
		c.mu.Unlock()
		return context.DeadlineExceeded
	}
	c.mu.Lock()
	c.last = guestssh.CloseOutcome{Complete: true}
	c.mu.Unlock()
	c.doneOnce.Do(func() { close(c.done) })
	return nil
}

func (c *retryCloseChannel) LastCloseOutcome() guestssh.CloseOutcome {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

type closeRetryHarness struct {
	svc       *app.SessionService
	channel   *retryCloseChannel
	actor     domain.ActorContext
	opened    *domain.SessionObservation
	mutations string
}

func newCloseRetryHarness(t *testing.T) closeRetryHarness {
	t.Helper()
	sd, err := statedir.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	channel := &retryCloseChannel{done: make(chan struct{})}
	mgr := sessions.NewManager(sd.SessionsDir(), retryCloseTransport{channel: channel}, time.Now)
	svc := app.NewSessionService(
		mgr,
		diagnosticReversibleSafety{},
		nil,
		audit.NewStore(sd.AuditDir()),
		receipt.NewStore(sd.ReceiptsDir()),
		approval.NewStore(sd.ApprovalsDir()),
	)
	scopes := domain.NewScopeSet(domain.ScopeSessionOpen, domain.ScopeSessionRead, domain.ScopeSessionClose)
	actor, err := domain.NewActorContext("agent:close-retry", "agent:close-retry", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	opened, _, err := svc.OpenSession(context.Background(), app.SessionOpenParams{
		Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Caller: actor,
		Reason: "open retry session", IdempotencyKey: "open-close-retry",
	})
	if err != nil {
		t.Fatal(err)
	}
	return closeRetryHarness{
		svc: svc, channel: channel, actor: actor, opened: opened,
		mutations: filepath.Join(sd.SessionsDir(), "mutations"),
	}
}

func assertPartiallyAppliedClose(t *testing.T, obs *domain.SessionObservation, rcpt *domain.Receipt, err error, sessionID domain.SessionID) {
	t.Helper()
	if err == nil || rcpt == nil || rcpt.Outcome.Status != domain.OutcomeFailed || obs == nil || obs.State != domain.SessionStateClosing {
		t.Fatalf("partially applied close = obs %+v receipt %+v err %v", obs, rcpt, err)
	}
	wantRollback := ""
	if rcpt.Class == domain.ClassReversibleMutation {
		wantRollback = "e4a523d4-6b99-4d62-a5e2-4752c0f20001"
	}
	if rcpt.RollbackRef != wantRollback || len(rcpt.EvidenceRefs) != 1 || rcpt.EvidenceRefs[0] != string(sessionID) {
		t.Fatalf("partially applied close omitted rollback/evidence: %+v", rcpt)
	}
}

func TestCloseSessionPartialEffectIsDurableExactRetryDoesNotDuplicateAndNewKeyRetriesCleanup(t *testing.T) {
	h := newCloseRetryHarness(t)
	firstParams := app.SessionCloseParams{
		SessionID: h.opened.ID, Caller: h.actor, Reason: "first bounded close",
		IdempotencyKey: "close-timeout-original", Timeout: time.Second,
	}
	firstObs, firstReceipt, firstErr := h.svc.CloseSession(context.Background(), firstParams)
	if !errors.Is(firstErr, context.DeadlineExceeded) {
		t.Fatalf("first close error = %v, want transport deadline", firstErr)
	}
	assertPartiallyAppliedClose(t, firstObs, firstReceipt, firstErr, h.opened.ID)
	reservation := mutationReservationByKey(t, h.mutations, firstParams.IdempotencyKey)
	if reservation.Result.EffectApplied == nil || !*reservation.Result.EffectApplied || reservation.Result.Observation == nil || reservation.Result.Observation.State != domain.SessionStateClosing {
		t.Fatalf("durable partial close result = %+v, want applied closing observation", reservation.Result)
	}
	retryObs, retryReceipt, retryErr := h.svc.CloseSession(context.Background(), firstParams)
	assertPartiallyAppliedClose(t, retryObs, retryReceipt, retryErr, h.opened.ID)
	if retryReceipt.ReceiptID != firstReceipt.ReceiptID {
		t.Fatalf("exact retry = obs %+v receipt %+v err %v", retryObs, retryReceipt, retryErr)
	}
	if got := h.channel.calls.Load(); got != 1 {
		t.Fatalf("transport calls after exact retry = %d, want 1", got)
	}

	closed, closeReceipt, err := h.svc.CloseSession(context.Background(), app.SessionCloseParams{
		SessionID: h.opened.ID, Caller: h.actor, Reason: "retry cleanup with new key",
		IdempotencyKey: "close-timeout-retry-new", Timeout: time.Second,
	})
	if err != nil || closeReceipt == nil || closed.State != domain.SessionStateClosed {
		t.Fatalf("new-key cleanup retry = obs %+v receipt %+v err %v", closed, closeReceipt, err)
	}
	if got := h.channel.calls.Load(); got != 2 {
		t.Fatalf("transport calls after new-key retry = %d, want 2", got)
	}
}

type approvedCloseRetryHarness struct {
	service       *app.SessionService
	channel       *retryCloseChannel
	approvalStore *approval.Store
	auditStore    *audit.Store
	opened        *domain.SessionObservation
	params        app.SessionCloseParams
	mutations     string
}

func newApprovedCloseRetryHarness(t *testing.T) approvedCloseRetryHarness {
	t.Helper()
	sd, err := statedir.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	channel := &retryCloseChannel{done: make(chan struct{})}
	manager := sessions.NewManager(sd.SessionsDir(), retryCloseTransport{channel: channel}, func() time.Time { return now })
	approvalStore := approval.NewStore(sd.ApprovalsDir())
	auditStore := audit.NewStore(sd.AuditDir())
	service := app.NewSessionService(
		manager,
		&destructiveAfterOpenSafety{},
		nil,
		auditStore,
		receipt.NewStore(sd.ReceiptsDir()),
		approvalStore,
		app.WithSessionClock(func() time.Time { return now }),
	)
	agentScopes := domain.NewScopeSet(domain.ScopeSessionOpen, domain.ScopeSessionRead, domain.ScopeSessionClose)
	agent, err := domain.NewActorContext("agent:mcp-local", "agent:mcp-local", agentScopes, agentScopes)
	if err != nil {
		t.Fatal(err)
	}
	operatorScopes := domain.NewScopeSet(domain.ScopeSessionAdmin)
	operator, err := domain.NewActorContext("operator:partial-close", "operator:partial-close", operatorScopes, operatorScopes)
	if err != nil {
		t.Fatal(err)
	}
	opened, _, err := service.OpenSession(context.Background(), app.SessionOpenParams{
		Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Caller: agent,
		Reason: "open approved partial close session", IdempotencyKey: "open-approved-partial-close",
	})
	if err != nil {
		t.Fatal(err)
	}
	grant, _, err := service.IssueSessionMutationApproval(context.Background(), app.SessionApprovalIssueParams{
		Kind: "session.close", Caller: operator, SessionID: opened.ID,
		Reason: "approved partial close", IdempotencyKey: "approved-partial-close", ValidFor: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	params := app.SessionCloseParams{
		SessionID: opened.ID, Caller: agent, Reason: "approved partial close",
		IdempotencyKey: "approved-partial-close", Deadline: grant.Deadline, ApprovalID: grant.ApprovalID,
	}
	return approvedCloseRetryHarness{
		service: service, channel: channel, approvalStore: approvalStore, auditStore: auditStore,
		opened: opened, params: params, mutations: filepath.Join(sd.SessionsDir(), "mutations"),
	}
}

func TestApprovedPartialCloseKeepsApprovalConsumedAndExactRetryDoesNotDuplicateEvidence(t *testing.T) {
	h := newApprovedCloseRetryHarness(t)

	firstObservation, firstReceipt, firstErr := h.service.CloseSession(context.Background(), h.params)
	if !errors.Is(firstErr, context.DeadlineExceeded) {
		t.Fatalf("first close error = %v, want transport deadline", firstErr)
	}
	assertPartiallyAppliedClose(t, firstObservation, firstReceipt, firstErr, h.opened.ID)
	if consumed, err := h.approvalStore.IsConsumed(h.params.ApprovalID); err != nil || !consumed {
		t.Fatalf("partially applied close approval consumed=%v err=%v", consumed, err)
	}
	reservation := mutationReservationByKey(t, h.mutations, h.params.IdempotencyKey)
	if reservation.Result.EffectApplied == nil || !*reservation.Result.EffectApplied || reservation.Result.Observation == nil || reservation.Result.Observation.State != domain.SessionStateClosing {
		t.Fatalf("durable approved partial close result = %+v", reservation.Result)
	}
	if err := h.auditStore.VerifyTerminalOutcome(*firstReceipt); err != nil {
		t.Fatalf("first close terminal evidence = %v", err)
	}

	retryObservation, retryReceipt, retryErr := h.service.CloseSession(context.Background(), h.params)
	assertPartiallyAppliedClose(t, retryObservation, retryReceipt, retryErr, h.opened.ID)
	if retryReceipt.ReceiptID != firstReceipt.ReceiptID {
		t.Fatalf("exact retry receipt = %s, want %s", retryReceipt.ReceiptID, firstReceipt.ReceiptID)
	}
	if got := h.channel.calls.Load(); got != 1 {
		t.Fatalf("transport close calls after exact retry = %d, want 1", got)
	}
	if err := h.auditStore.VerifyTerminalOutcome(*retryReceipt); err != nil {
		t.Fatalf("exact retry duplicated or corrupted terminal evidence: %v", err)
	}
}
