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

type incompleteOpenTransport struct {
	dialCalls         atomic.Int32
	closeCalls        atomic.Int32
	supervisorStarted chan struct{}
	releaseSupervisor chan struct{}
	supervisorDone    chan struct{}
	supervisorOnce    sync.Once
}

func (t *incompleteOpenTransport) Dial(context.Context, domain.MachineRef, uint16, uint16, string) (guestssh.Channel, error) {
	t.dialCalls.Add(1)
	return &incompleteOpenChannel{transport: t}, nil
}

type incompleteOpenChannel struct {
	transport *incompleteOpenTransport
	mu        sync.Mutex
	last      guestssh.CloseOutcome
}

func (*incompleteOpenChannel) Read([]byte) (int, error)                   { return 0, io.EOF }
func (*incompleteOpenChannel) Write(context.Context, []byte) (int, error) { return 0, nil }
func (*incompleteOpenChannel) SendControl(context.Context, domain.ControlKey) (guestssh.ControlResult, error) {
	return guestssh.ControlResult{AcceptedBytes: 1, EffectApplied: true}, nil
}
func (*incompleteOpenChannel) Resize(uint16, uint16) error { return nil }
func (c *incompleteOpenChannel) Close(context.Context) error {
	call := c.transport.closeCalls.Add(1)
	if call == 2 && c.transport.releaseSupervisor != nil {
		c.transport.supervisorOnce.Do(func() { close(c.transport.supervisorStarted) })
		<-c.transport.releaseSupervisor
		defer close(c.transport.supervisorDone)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if call >= 3 {
		c.last = guestssh.CloseOutcome{Complete: true}
		return nil
	}
	c.last = guestssh.CloseOutcome{Complete: false, Err: context.DeadlineExceeded}
	return c.last.Err
}
func (c *incompleteOpenChannel) LastCloseOutcome() guestssh.CloseOutcome {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}
func (*incompleteOpenChannel) Wait() (int, error) { return 0, nil }

func assertIncompleteOpenFailure(t *testing.T, obs *domain.SessionObservation, rcpt *domain.Receipt, err error) {
	t.Helper()
	if obs != nil {
		t.Fatalf("first open observation = %+v, want nil", obs)
	}
	var openFailure *sessions.OpenFailure
	if !errors.As(err, &openFailure) {
		t.Fatalf("first open error = %v, want typed open failure", err)
	}
	if rcpt == nil {
		t.Fatal("first open receipt is nil")
	}
	if rcpt.Outcome.Status != domain.OutcomeFailed {
		t.Fatalf("durable outcome = %+v, want failed", rcpt.Outcome)
	}
	if rcpt.RollbackRef == "" {
		t.Fatal("durable outcome is missing rollback reference")
	}
	if len(rcpt.EvidenceRefs) != 1 {
		t.Fatalf("evidence refs = %v, want one redacted cleanup reference", rcpt.EvidenceRefs)
	}
	if rcpt.EvidenceRefs[0] != "session-channel-cleanup-incomplete" {
		t.Fatalf("evidence refs = %v, want redacted cleanup evidence", rcpt.EvidenceRefs)
	}
}

func assertOpenTransportCalls(t *testing.T, transport *incompleteOpenTransport, wantClose int32) {
	t.Helper()
	if got := transport.dialCalls.Load(); got != 1 {
		t.Fatalf("transport dial calls = %d, want 1", got)
	}
	if got := transport.closeCalls.Load(); got != wantClose {
		t.Fatalf("transport close calls = %d, want %d", got, wantClose)
	}
}

func assertIncompleteOpenRetry(t *testing.T, svc *app.SessionService, params app.SessionOpenParams, firstReceipt *domain.Receipt) {
	t.Helper()
	retryObs, retryReceipt, retryErr := svc.OpenSession(context.Background(), params)
	if retryObs != nil {
		t.Fatalf("exact retry observation = %+v, want nil", retryObs)
	}
	if retryReceipt == nil {
		t.Fatal("exact retry receipt is nil")
	}
	if retryReceipt.ReceiptID != firstReceipt.ReceiptID {
		t.Fatalf("exact retry receipt = %s, want %s", retryReceipt.ReceiptID, firstReceipt.ReceiptID)
	}
	if retryErr == nil {
		t.Fatal("exact retry unexpectedly succeeded")
	}
}

func TestSessionOpenIncompletePostEffectCleanupIsDurableAndNotRedialed(t *testing.T) {
	sd, err := statedir.Resolve(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	transport := &incompleteOpenTransport{
		supervisorStarted: make(chan struct{}),
		releaseSupervisor: make(chan struct{}),
		supervisorDone:    make(chan struct{}),
	}
	mgr := sessions.NewManager(
		sd.SessionsDir(),
		transport,
		time.Now,
		sessions.WithSessionIDGenerator(func() (domain.SessionID, error) {
			return "", errors.New("synthetic session ID generation failure")
		}),
	)
	svc := app.NewSessionService(
		mgr,
		diagnosticReversibleSafety{},
		nil,
		audit.NewStore(sd.AuditDir()),
		receipt.NewStore(sd.ReceiptsDir()),
		approval.NewStore(sd.ApprovalsDir()),
	)
	scopes := domain.NewScopeSet(domain.ScopeSessionOpen)
	actor, err := domain.NewActorContext("agent:open-effect", "agent:open-effect", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	params := app.SessionOpenParams{
		Target:         "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Caller:         actor,
		Reason:         "record incomplete post-effect cleanup",
		IdempotencyKey: "incomplete-post-effect-open",
		Timeout:        time.Second,
	}

	obs, firstReceipt, firstErr := svc.OpenSession(context.Background(), params)
	assertIncompleteOpenFailure(t, obs, firstReceipt, firstErr)
	select {
	case <-transport.supervisorStarted:
	case <-time.After(time.Second):
		t.Fatal("supervised cleanup did not start")
	}
	assertOpenTransportCalls(t, transport, 2)
	assertIncompleteOpenRetry(t, svc, params, firstReceipt)
	assertOpenTransportCalls(t, transport, 2)
	close(transport.releaseSupervisor)
	select {
	case <-transport.supervisorDone:
	case <-time.After(time.Second):
		t.Fatal("supervised cleanup did not finish")
	}
	if err := mgr.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown cleanup retry failed: %v", err)
	}
	assertOpenTransportCalls(t, transport, 3)
	if err := mgr.Shutdown(context.Background()); err != nil {
		t.Fatalf("repeated shutdown after cleanup failed: %v", err)
	}
	assertOpenTransportCalls(t, transport, 3)
}

func TestSessionOpenCompletedPostEffectCleanupDoesNotClaimLiveSession(t *testing.T) {
	sd, err := statedir.Resolve(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	transport := &trackingTransport{}
	mgr := sessions.NewManager(
		sd.SessionsDir(),
		transport,
		time.Now,
		sessions.WithPublishOpenHook(func() error { return errors.New("synthetic publication failure") }),
	)
	svc := app.NewSessionService(
		mgr,
		diagnosticReversibleSafety{},
		nil,
		audit.NewStore(sd.AuditDir()),
		receipt.NewStore(sd.ReceiptsDir()),
		approval.NewStore(sd.ApprovalsDir()),
	)
	scopes := domain.NewScopeSet(domain.ScopeSessionOpen, domain.ScopeSessionRead)
	actor, err := domain.NewActorContext("agent:open-cleanup", "agent:open-cleanup", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}

	obs, rcpt, err := svc.OpenSession(context.Background(), app.SessionOpenParams{
		Target:         "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Caller:         actor,
		Reason:         "verify completed post-effect cleanup",
		IdempotencyKey: "completed-post-effect-cleanup",
		Timeout:        time.Second,
	})
	if obs != nil || err == nil || rcpt == nil || rcpt.Outcome.Status != domain.OutcomeFailed {
		t.Fatalf("open = obs %+v receipt %+v err %v", obs, rcpt, err)
	}
	if rcpt.RollbackRef != "" || len(rcpt.EvidenceRefs) != 0 {
		t.Fatalf("completed cleanup receipt claims live effect: rollback %q evidence %v", rcpt.RollbackRef, rcpt.EvidenceRefs)
	}
	if atomic.LoadInt32(&transport.dialCalls) != 1 || atomic.LoadInt32(&transport.closeCalls) != 1 {
		t.Fatalf("transport calls = dial %d close %d, want 1/1", transport.dialCalls, transport.closeCalls)
	}
	listed, listErr := mgr.List(context.Background(), actor, "")
	if listErr != nil || len(listed) != 0 {
		t.Fatalf("published sessions = %+v err %v, want none", listed, listErr)
	}
}
