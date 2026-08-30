package sessions_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
	"github.com/Horcag/agent-machine-control/internal/sessions"
)

var errSyntheticCleanup = errors.New("synthetic cleanup failure")

type lifecycleTransport struct{ channel *lifecycleChannel }

func (t lifecycleTransport) Dial(context.Context, domain.MachineRef, uint16, uint16, string) (guestssh.Channel, error) {
	return t.channel, nil
}

type lifecycleChannel struct {
	mu           sync.Mutex
	waitRelease  chan struct{}
	readRelease  chan struct{}
	closeStarted chan struct{}
	allowClose   chan struct{}
	waitOnce     sync.Once
	readOnce     sync.Once
	startOnce    sync.Once
	waitCode     int
	waitErr      error
	closePlan    []guestssh.CloseOutcome
	lastOutcome  guestssh.CloseOutcome
	closeCalls   atomic.Int32
}

func newLifecycleChannel(plan ...guestssh.CloseOutcome) *lifecycleChannel {
	return &lifecycleChannel{
		waitRelease:  make(chan struct{}),
		readRelease:  make(chan struct{}),
		closeStarted: make(chan struct{}),
		closePlan:    plan,
	}
}

func (c *lifecycleChannel) Read([]byte) (int, error) {
	<-c.readRelease
	return 0, io.EOF
}

func (c *lifecycleChannel) Write(_ context.Context, data []byte) (int, error) { return len(data), nil }
func (c *lifecycleChannel) SendControl(context.Context, domain.ControlKey) (int, error) {
	return 1, nil
}
func (c *lifecycleChannel) Resize(uint16, uint16) error { return nil }

func (c *lifecycleChannel) Close(ctx context.Context) error {
	c.closeCalls.Add(1)
	c.startOnce.Do(func() { close(c.closeStarted) })
	if c.allowClose != nil {
		select {
		case <-c.allowClose:
		case <-ctx.Done():
			c.mu.Lock()
			c.lastOutcome = guestssh.CloseOutcome{Complete: false, Err: ctx.Err()}
			c.mu.Unlock()
			return ctx.Err()
		}
	}
	c.mu.Lock()
	outcome := guestssh.CloseOutcome{Complete: true}
	if len(c.closePlan) > 0 {
		outcome = c.closePlan[0]
		c.closePlan = c.closePlan[1:]
	}
	c.lastOutcome = outcome
	c.mu.Unlock()
	if outcome.Complete {
		c.waitOnce.Do(func() { close(c.waitRelease) })
		c.readOnce.Do(func() { close(c.readRelease) })
	}
	return outcome.Err
}

func (c *lifecycleChannel) LastCloseOutcome() guestssh.CloseOutcome {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastOutcome
}

func (c *lifecycleChannel) Wait() (int, error) {
	<-c.waitRelease
	return c.waitCode, c.waitErr
}

func lifecycleActor(t *testing.T) domain.ActorContext {
	t.Helper()
	scopes := domain.NewScopeSet(domain.ScopeSessionOpen, domain.ScopeSessionRead, domain.ScopeSessionWrite, domain.ScopeSessionClose)
	actor, err := domain.NewActorContext("agent:lifecycle", "agent:lifecycle", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	return actor
}

func openLifecycleSession(t *testing.T, dir string, channel *lifecycleChannel) (*sessions.Manager, domain.ActorContext, domain.SessionID) {
	t.Helper()
	actor := lifecycleActor(t)
	mgr := sessions.NewManager(dir, lifecycleTransport{channel: channel}, time.Now)
	obs, err := mgr.Open(context.Background(), domain.Operation{
		Kind: "session.open", Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Actor: actor,
		Reason: "lifecycle regression", IdempotencyKey: "open-lifecycle-regression",
	}, 80, 24, "xterm-256color")
	if err != nil {
		t.Fatal(err)
	}
	return mgr, actor, obs.ID
}

func TestManagerCloseRetriesIncompleteCleanup(t *testing.T) {
	channel := newLifecycleChannel(
		guestssh.CloseOutcome{Complete: false, Err: context.DeadlineExceeded},
		guestssh.CloseOutcome{Complete: true},
	)
	mgr, actor, id := openLifecycleSession(t, t.TempDir(), channel)

	first, err := mgr.Close(context.Background(), id, actor, "first close", false)
	if !errors.Is(err, context.DeadlineExceeded) || first.State != domain.SessionStateClosing {
		t.Fatalf("first close = state %q err %v, want retryable closing timeout", first.State, err)
	}
	second, err := mgr.Close(context.Background(), id, actor, "retry close", false)
	if err != nil || second.State != domain.SessionStateClosed {
		t.Fatalf("retry close = state %q err %v, want closed", second.State, err)
	}
	if got := channel.closeCalls.Load(); got != 2 {
		t.Fatalf("transport close calls = %d, want 2", got)
	}
}

func TestManagerReplaysCompletedCleanupFailure(t *testing.T) {
	channel := newLifecycleChannel(guestssh.CloseOutcome{Complete: true, Err: errSyntheticCleanup})
	dir := t.TempDir()
	mgr, actor, id := openLifecycleSession(t, dir, channel)

	first, err := mgr.Close(context.Background(), id, actor, "close", false)
	if !errors.Is(err, errSyntheticCleanup) || first.State != domain.SessionStateFailed {
		t.Fatalf("first close = state %q err %v, want failed cleanup evidence", first.State, err)
	}
	replayed, err := mgr.Close(context.Background(), id, actor, "repeat close", false)
	if !errors.Is(err, errSyntheticCleanup) || replayed.State != domain.SessionStateFailed {
		t.Fatalf("repeated close = state %q err %v, want cached failure", replayed.State, err)
	}
	if err := mgr.Shutdown(context.Background()); !errors.Is(err, errSyntheticCleanup) {
		t.Fatalf("shutdown error = %v, want cached cleanup failure", err)
	}
	if got := channel.closeCalls.Load(); got != 1 {
		t.Fatalf("transport close calls = %d, want exactly 1", got)
	}
	restarted := sessions.NewManager(dir, nil, time.Now)
	loaded, err := restarted.Get(context.Background(), id, actor)
	if err != nil || loaded.State != domain.SessionStateFailed || loaded.ErrorMessage != "transport_close_failed" {
		t.Fatalf("restart observation = %+v err %v", loaded, err)
	}
}

func TestManagerBareEOFCleanupClosesCleanly(t *testing.T) {
	channel := newLifecycleChannel(guestssh.CloseOutcome{Complete: true, Err: io.EOF})
	dir := t.TempDir()
	mgr, actor, id := openLifecycleSession(t, dir, channel)

	first, err := mgr.Close(context.Background(), id, actor, "close", false)
	if err != nil || first.State != domain.SessionStateClosed || first.ErrorMessage != "" {
		t.Fatalf("first close = state %q category %q err %v, want clean closed", first.State, first.ErrorMessage, err)
	}
	replayed, err := mgr.Close(context.Background(), id, actor, "repeat close", false)
	if err != nil || replayed.State != domain.SessionStateClosed {
		t.Fatalf("repeated close = state %q err %v, want cached clean close", replayed.State, err)
	}
	if err := mgr.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown replayed bare EOF as failure: %v", err)
	}
	if got := channel.closeCalls.Load(); got != 1 {
		t.Fatalf("transport close calls = %d, want exactly 1", got)
	}
	loaded, err := sessions.NewManager(dir, nil, time.Now).Get(context.Background(), id, actor)
	if err != nil || loaded.State != domain.SessionStateClosed || loaded.ErrorMessage != "" {
		t.Fatalf("restart observation = %+v err %v, want clean closed truth", loaded, err)
	}
}

func TestManagerCompositeEOFCleanupFailureRemainsStable(t *testing.T) {
	closeErr := errors.Join(io.EOF, errSyntheticCleanup)
	channel := newLifecycleChannel(guestssh.CloseOutcome{Complete: true, Err: closeErr})
	dir := t.TempDir()
	mgr, actor, id := openLifecycleSession(t, dir, channel)

	first, err := mgr.Close(context.Background(), id, actor, "close", false)
	if first.State != domain.SessionStateFailed || first.ErrorMessage != "transport_close_failed" {
		t.Fatalf("first close = %+v, want failed cleanup truth", first)
	}
	assertStableCompositeCleanupError(t, err, closeErr.Error())

	replayed, err := mgr.Close(context.Background(), id, actor, "repeat close", false)
	if replayed.State != domain.SessionStateFailed {
		t.Fatalf("repeated close state = %q, want failed", replayed.State)
	}
	assertStableCompositeCleanupError(t, err, closeErr.Error())
	assertStableCompositeCleanupError(t, mgr.Shutdown(context.Background()), closeErr.Error())
	if got := channel.closeCalls.Load(); got != 1 {
		t.Fatalf("transport close calls = %d, want exactly 1", got)
	}

	loaded, err := sessions.NewManager(dir, nil, time.Now).Get(context.Background(), id, actor)
	if err != nil || loaded.State != domain.SessionStateFailed || loaded.ErrorMessage != "transport_close_failed" {
		t.Fatalf("restart observation = %+v err %v, want persisted failed cleanup truth", loaded, err)
	}
}

func assertStableCompositeCleanupError(t *testing.T, err error, wantMessage string) {
	t.Helper()
	if !errors.Is(err, io.EOF) || !errors.Is(err, errSyntheticCleanup) {
		t.Fatalf("cleanup error = %v, want EOF and synthetic cleanup components", err)
	}
	if err.Error() != wantMessage {
		t.Fatalf("cleanup error message = %q, want stable %q", err, wantMessage)
	}
}

func TestManagerNaturalExitTruthAndCleanup(t *testing.T) {
	tests := []struct {
		name         string
		waitErr      error
		closeOutcome guestssh.CloseOutcome
		wantState    domain.SessionState
		wantCategory string
	}{
		{name: "normal nonzero exit", closeOutcome: guestssh.CloseOutcome{Complete: true}, wantState: domain.SessionStateClosed},
		{name: "wait I/O failure", waitErr: errors.New("synthetic wait I/O failure"), closeOutcome: guestssh.CloseOutcome{Complete: true}, wantState: domain.SessionStateFailed, wantCategory: "transport_wait_failed"},
		{name: "cleanup failure", closeOutcome: guestssh.CloseOutcome{Complete: true, Err: errSyntheticCleanup}, wantState: domain.SessionStateFailed, wantCategory: "transport_cleanup_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel := newLifecycleChannel(tt.closeOutcome)
			channel.waitCode = 23
			channel.waitErr = tt.waitErr
			mgr, actor, id := openLifecycleSession(t, t.TempDir(), channel)
			channel.waitOnce.Do(func() { close(channel.waitRelease) })
			<-channel.closeStarted

			var obs *domain.SessionObservation
			for range 100 {
				obs, _ = mgr.Get(context.Background(), id, actor)
				if obs.State.IsTerminal() {
					break
				}
				time.Sleep(time.Millisecond)
			}
			_ = mgr.Shutdown(context.Background())
			if obs.State != tt.wantState || obs.ErrorMessage != tt.wantCategory || obs.ExitCode == nil || *obs.ExitCode != 23 {
				t.Fatalf("natural exit observation = %+v, want state %q category %q exit 23", obs, tt.wantState, tt.wantCategory)
			}
			if got := channel.closeCalls.Load(); got != 1 {
				t.Fatalf("cleanup calls = %d, want 1", got)
			}
		})
	}
}

func TestManagerExplicitCloseAndShutdownShareOneFinalizer(t *testing.T) {
	channel := newLifecycleChannel(guestssh.CloseOutcome{Complete: true})
	channel.allowClose = make(chan struct{})
	mgr, actor, id := openLifecycleSession(t, t.TempDir(), channel)

	closeDone := make(chan error, 1)
	go func() {
		_, err := mgr.Close(context.Background(), id, actor, "explicit close", false)
		closeDone <- err
	}()
	<-channel.closeStarted
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- mgr.Shutdown(context.Background()) }()
	close(channel.allowClose)
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
	if got := channel.closeCalls.Load(); got != 1 {
		t.Fatalf("transport close calls = %d, want exactly 1", got)
	}
}
