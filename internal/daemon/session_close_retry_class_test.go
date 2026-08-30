package daemon_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

type retryClassTransport struct{ channel *retryClassChannel }

func (t retryClassTransport) Dial(context.Context, domain.MachineRef, uint16, uint16, string) (guestssh.Channel, error) {
	return t.channel, nil
}

type retryClassChannel struct {
	done     chan struct{}
	doneOnce sync.Once
	lastMu   sync.Mutex
	last     guestssh.CloseOutcome
	calls    atomic.Int32
}

func (c *retryClassChannel) Read([]byte) (int, error)                   { <-c.done; return 0, io.EOF }
func (c *retryClassChannel) Write(context.Context, []byte) (int, error) { return 0, nil }
func (c *retryClassChannel) SendControl(context.Context, domain.ControlKey) (guestssh.ControlResult, error) {
	return guestssh.ControlResult{}, nil
}
func (c *retryClassChannel) Resize(uint16, uint16) error { return nil }
func (c *retryClassChannel) Wait() (int, error)          { <-c.done; return 0, nil }

func (c *retryClassChannel) Close(context.Context) error {
	if c.calls.Add(1) == 1 {
		c.lastMu.Lock()
		c.last = guestssh.CloseOutcome{Complete: false, Err: context.DeadlineExceeded}
		c.lastMu.Unlock()
		return context.DeadlineExceeded
	}
	c.lastMu.Lock()
	c.last = guestssh.CloseOutcome{Complete: true}
	c.lastMu.Unlock()
	c.doneOnce.Do(func() { close(c.done) })
	return nil
}

func (c *retryClassChannel) LastCloseOutcome() guestssh.CloseOutcome {
	c.lastMu.Lock()
	defer c.lastMu.Unlock()
	return c.last
}

func TestDaemonClosePartialEffectExactRetryPreservesGatewayTimeout(t *testing.T) {
	dir := t.TempDir()
	channel := &retryClassChannel{done: make(chan struct{})}
	keyProvider := &guestssh.MockKeyProvider{MachineConfig: &guestssh.MachineSSHConfig{
		ExternalEffectsContained: true,
		RollbackCheckpointID:     "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
	}}
	srv, err := daemon.NewServer(daemon.Config{
		StateDir: dir, ListenAddr: "127.0.0.1:0", Backend: &mockDaemonBackend{},
		Transport: retryClassTransport{channel: channel}, KeyProvider: keyProvider,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := srv.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	}()
	token, err := auth.ReadTokenFile(filepath.Join(dir, "auth"), auth.TokenTypeOperator)
	if err != nil {
		t.Fatal(err)
	}

	status, data := doJSONReq(t, http.MethodPost, srv.Endpoint()+"/v1/sessions", token, daemon.SessionOpenRequest{
		Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "open retry class session", IdempotencyKey: "retry-class-open",
	})
	if status != http.StatusOK {
		t.Fatalf("open status = %d, body = %s", status, data)
	}
	var opened daemon.SessionOpenResponse
	if err := json.Unmarshal(data, &opened); err != nil {
		t.Fatal(err)
	}
	closeURL := srv.Endpoint() + "/v1/sessions/" + opened.Session.SessionID + "/close"
	request := daemon.SessionCloseRequest{Reason: "bounded close", IdempotencyKey: "retry-class-close"}
	for attempt := 1; attempt <= 2; attempt++ {
		status, data = doJSONReq(t, http.MethodPost, closeURL, token, request)
		if status != http.StatusGatewayTimeout {
			t.Fatalf("close attempt %d status = %d, want 504; body = %s", attempt, status, data)
		}
	}
	if got := channel.calls.Load(); got != 1 {
		t.Fatalf("transport close calls after exact retry = %d, want 1", got)
	}
}
