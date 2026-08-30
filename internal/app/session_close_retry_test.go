package app_test

import (
	"context"
	"errors"
	"io"
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

type retryCloseChannel struct {
	mu       sync.Mutex
	done     chan struct{}
	doneOnce sync.Once
	last     guestssh.CloseOutcome
	calls    atomic.Int32
}

func (c *retryCloseChannel) Read([]byte) (int, error)                          { <-c.done; return 0, io.EOF }
func (c *retryCloseChannel) Write(_ context.Context, data []byte) (int, error) { return len(data), nil }
func (c *retryCloseChannel) SendControl(context.Context, domain.ControlKey) (int, error) {
	return 1, nil
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
	svc     *app.SessionService
	channel *retryCloseChannel
	actor   domain.ActorContext
	opened  *domain.SessionObservation
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
	return closeRetryHarness{svc: svc, channel: channel, actor: actor, opened: opened}
}

func assertAbortedClose(t *testing.T, obs *domain.SessionObservation, rcpt *domain.Receipt, err error) {
	t.Helper()
	if !errors.Is(err, context.DeadlineExceeded) || rcpt == nil || rcpt.Outcome.Status != domain.OutcomeAborted || obs == nil || obs.State != domain.SessionStateClosing {
		t.Fatalf("aborted close = obs %+v receipt %+v err %v", obs, rcpt, err)
	}
	if rcpt.RollbackRef != "" || len(rcpt.EvidenceRefs) != 0 {
		t.Fatalf("zero-effect aborted close exposed rollback/evidence: %+v", rcpt)
	}
}

func TestCloseSessionExactAbortedRetryHasNoEffectAndNewKeyRetriesCleanup(t *testing.T) {
	h := newCloseRetryHarness(t)
	firstParams := app.SessionCloseParams{
		SessionID: h.opened.ID, Caller: h.actor, Reason: "first bounded close",
		IdempotencyKey: "close-timeout-original", Timeout: time.Second,
	}
	firstObs, firstReceipt, firstErr := h.svc.CloseSession(context.Background(), firstParams)
	assertAbortedClose(t, firstObs, firstReceipt, firstErr)
	retryObs, retryReceipt, retryErr := h.svc.CloseSession(context.Background(), firstParams)
	assertAbortedClose(t, retryObs, retryReceipt, retryErr)
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
