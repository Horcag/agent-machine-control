package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

type shutdownAdmissionTransport struct {
	dials        atomic.Int32
	closeStarted chan struct{}
	closeRelease chan struct{}
}

func (t *shutdownAdmissionTransport) Dial(context.Context, domain.MachineRef, uint16, uint16, string) (guestssh.Channel, error) {
	t.dials.Add(1)
	return &shutdownAdmissionChannel{closeStarted: t.closeStarted, closeRelease: t.closeRelease, done: make(chan struct{})}, nil
}

type shutdownAdmissionChannel struct {
	closeStarted chan struct{}
	closeRelease chan struct{}
	done         chan struct{}
	startOnce    sync.Once
	doneOnce     sync.Once
}

func (c *shutdownAdmissionChannel) Read([]byte) (int, error)                   { <-c.done; return 0, io.EOF }
func (c *shutdownAdmissionChannel) Write(context.Context, []byte) (int, error) { return 0, nil }
func (c *shutdownAdmissionChannel) SendControl(context.Context, domain.ControlKey) (guestssh.ControlResult, error) {
	return guestssh.ControlResult{AcceptedBytes: 1, EffectApplied: true}, nil
}
func (c *shutdownAdmissionChannel) Resize(uint16, uint16) error { return nil }
func (c *shutdownAdmissionChannel) Close(ctx context.Context) error {
	c.startOnce.Do(func() { close(c.closeStarted) })
	select {
	case <-c.closeRelease:
		c.doneOnce.Do(func() { close(c.done) })
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *shutdownAdmissionChannel) LastCloseOutcome() guestssh.CloseOutcome {
	select {
	case <-c.done:
		return guestssh.CloseOutcome{Complete: true}
	default:
		return guestssh.CloseOutcome{}
	}
}
func (c *shutdownAdmissionChannel) Wait() (int, error) { <-c.done; return 0, nil }

func TestServerShutdownRejectsSessionOpenWhileSessionDrainIsHeld(t *testing.T) {
	dir := t.TempDir()
	transport := &shutdownAdmissionTransport{closeStarted: make(chan struct{}), closeRelease: make(chan struct{})}
	kp := &guestssh.MockKeyProvider{MachineConfig: &guestssh.MachineSSHConfig{
		ExternalEffectsContained: true,
		RollbackCheckpointID:     "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
	}}
	srv, err := daemon.NewServer(daemon.Config{StateDir: dir, ListenAddr: "127.0.0.1:0", Backend: &mockDaemonBackend{}, Transport: transport, KeyProvider: kp})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	token, err := auth.ReadTokenFile(filepath.Join(dir, "auth"), auth.TokenTypeOperator)
	if err != nil {
		t.Fatal(err)
	}
	httpTransport := &http.Transport{MaxConnsPerHost: 1, MaxIdleConnsPerHost: 1}
	client := &http.Client{Transport: httpTransport}
	defer httpTransport.CloseIdleConnections()
	post := func(key, bearer string) (*http.Response, error) {
		body, _ := json.Marshal(daemon.SessionOpenRequest{Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "shutdown admission regression", IdempotencyKey: key})
		req, _ := http.NewRequest(http.MethodPost, srv.Endpoint()+"/v1/sessions", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Content-Type", "application/json")
		return client.Do(req)
	}
	first, err := post("shutdown-admission-first", token)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, first.Body)
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first open status = %d", first.StatusCode)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- srv.Shutdown(shutdownCtx) }()
	select {
	case <-transport.closeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("session shutdown drain did not start")
	}
	unauthorized, err := post("shutdown-admission-unauthorized", "invalid")
	if err != nil {
		t.Fatalf("unauthorized post-cutover request failed: %v", err)
	}
	_, _ = io.Copy(io.Discard, unauthorized.Body)
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("post-cutover unauthorized status = %d, want 401", unauthorized.StatusCode)
	}
	second, err := post("shutdown-admission-second", token)
	if err != nil {
		t.Fatalf("post-cutover request did not reach accepted connection: %v", err)
	}
	_, _ = io.Copy(io.Discard, second.Body)
	second.Body.Close()
	if second.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("post-cutover status = %d, want 503", second.StatusCode)
	}
	if got := transport.dials.Load(); got != 1 {
		t.Fatalf("post-cutover dial calls = %d, want 1 total", got)
	}
	close(transport.closeRelease)
	if err := <-shutdownDone; err != nil {
		t.Fatal(err)
	}
}
