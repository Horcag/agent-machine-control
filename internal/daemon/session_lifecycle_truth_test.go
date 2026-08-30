package daemon_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

type daemonLifecycleTransport struct{ channel *daemonLifecycleChannel }

func (t daemonLifecycleTransport) Dial(context.Context, domain.MachineRef, uint16, uint16, string) (guestssh.Channel, error) {
	return t.channel, nil
}

type daemonLifecycleChannel struct {
	mu          sync.Mutex
	waitRelease chan struct{}
	readRelease chan struct{}
	waitOnce    sync.Once
	readOnce    sync.Once
	waitErr     error
	cleanupErr  error
	last        guestssh.CloseOutcome
}

func newDaemonLifecycleChannel(waitErr, cleanupErr error) *daemonLifecycleChannel {
	return &daemonLifecycleChannel{
		waitRelease: make(chan struct{}), readRelease: make(chan struct{}),
		waitErr: waitErr, cleanupErr: cleanupErr,
	}
}

func (c *daemonLifecycleChannel) Read([]byte) (int, error) { <-c.readRelease; return 0, io.EOF }
func (c *daemonLifecycleChannel) Write(_ context.Context, data []byte) (int, error) {
	return len(data), nil
}
func (c *daemonLifecycleChannel) SendControl(context.Context, domain.ControlKey) (int, error) {
	return 1, nil
}
func (c *daemonLifecycleChannel) Resize(uint16, uint16) error { return nil }
func (c *daemonLifecycleChannel) Wait() (int, error) {
	<-c.waitRelease
	return 19, c.waitErr
}
func (c *daemonLifecycleChannel) Close(context.Context) error {
	c.mu.Lock()
	c.last = guestssh.CloseOutcome{Complete: true, Err: c.cleanupErr}
	c.mu.Unlock()
	c.readOnce.Do(func() { close(c.readRelease) })
	return c.cleanupErr
}
func (c *daemonLifecycleChannel) LastCloseOutcome() guestssh.CloseOutcome {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}
func (c *daemonLifecycleChannel) releaseWait() { c.waitOnce.Do(func() { close(c.waitRelease) }) }

func startDaemonLifecycleServer(t *testing.T, channel *daemonLifecycleChannel) (*daemon.Server, string, string) {
	t.Helper()
	dir := t.TempDir()
	const checkpoint = "e4a523d4-6b99-4d62-a5e2-4752c0f20001"
	keyProvider := &guestssh.MockKeyProvider{MachineConfig: &guestssh.MachineSSHConfig{
		Endpoint: "192.0.2.40:22", User: "synthetic", DefaultKeyAlias: "default",
		PinnedHostKeySHA256: "c3ludGhldGlj", ExternalEffectsContained: true, RollbackCheckpointID: checkpoint,
	}}
	srv, err := daemon.NewServer(daemon.Config{
		StateDir: dir, ListenAddr: "127.0.0.1:0", Backend: &mockDaemonBackend{},
		Transport: daemonLifecycleTransport{channel: channel}, KeyProvider: keyProvider,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	sd, err := statedir.Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	token, err := auth.ReadTokenFile(sd.AuthDir(), auth.TokenTypeOperator)
	if err != nil {
		t.Fatal(err)
	}
	return srv, srv.Endpoint(), token
}

func TestDaemonReportsNaturalExitAndTransportFailuresTruthfully(t *testing.T) {
	tests := []struct {
		name         string
		waitErr      error
		cleanupErr   error
		wantState    string
		wantCategory string
	}{
		{name: "natural exit", wantState: "closed"},
		{name: "wait I/O failure", waitErr: errors.New("synthetic wait failure"), wantState: "failed", wantCategory: "transport_wait_failed"},
		{name: "cleanup failure", cleanupErr: errors.New("synthetic cleanup failure"), wantState: "failed", wantCategory: "transport_cleanup_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel := newDaemonLifecycleChannel(tt.waitErr, tt.cleanupErr)
			srv, endpoint, token := startDaemonLifecycleServer(t, channel)
			defer func() { _ = srv.Shutdown(context.Background()) }()

			status, data := doJSONReq(t, http.MethodPost, endpoint+"/v1/sessions", token, daemon.SessionOpenRequest{
				Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "daemon lifecycle truth",
				IdempotencyKey: "daemon-lifecycle-open",
			})
			if status != http.StatusOK {
				t.Fatalf("open status %d: %s", status, data)
			}
			var opened daemon.SessionOpenResponse
			if err := json.Unmarshal(data, &opened); err != nil {
				t.Fatal(err)
			}
			channel.releaseWait()

			deadline := time.Now().Add(2 * time.Second)
			var current daemon.SessionOpenResponse
			for time.Now().Before(deadline) {
				status, data = doJSONReq(t, http.MethodGet, endpoint+"/v1/sessions/"+opened.Session.SessionID, token, nil)
				if status == http.StatusOK && json.Unmarshal(data, &current) == nil && (current.Session.State == "closed" || current.Session.State == "failed") {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if current.Session.State != tt.wantState || current.Session.ErrorMessage != tt.wantCategory || current.Session.ExitCode == nil || *current.Session.ExitCode != 19 {
				t.Fatalf("daemon observation = %+v, want state %q category %q exit 19", current.Session, tt.wantState, tt.wantCategory)
			}
		})
	}
}
