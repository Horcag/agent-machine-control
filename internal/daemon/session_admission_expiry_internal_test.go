package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

// The parent owns deadline delivery so filesystem latency cannot expire admission
// before the safety gate. The separate HTTP timeout cases exercise real timers.
type gatedAdmissionDeadline struct {
	context.Context
	deadline time.Time
	start    sync.Once
	done     chan struct{}
	expired  atomic.Bool
}

func (c *gatedAdmissionDeadline) Deadline() (time.Time, bool) {
	c.start.Do(func() { c.deadline = time.Now().Add(250 * time.Millisecond) })
	return c.deadline, true
}
func (c *gatedAdmissionDeadline) Done() <-chan struct{} { return c.done }
func (c *gatedAdmissionDeadline) Err() error {
	if c.expired.Load() {
		return context.DeadlineExceeded
	}
	return nil
}
func (c *gatedAdmissionDeadline) expire() {
	if c.expired.CompareAndSwap(false, true) {
		close(c.done)
	}
}

type gatedAdmissionBackend struct {
	exactRetryBackend
	gate func(context.Context)
}

func (b *gatedAdmissionBackend) ListCheckpoints(ctx context.Context, id string) ([]domain.CheckpointObservation, error) {
	if b.gate != nil {
		b.gate(ctx)
		return nil, ctx.Err()
	}
	return []domain.CheckpointObservation{{
		ID: "e4a523d4-6b99-4d62-a5e2-4752c0f20001", VMID: id, Name: "synthetic-base",
		CheckpointType: "Standard", CreatedAt: time.Now().UTC(), ObservedAt: time.Now().UTC(), ObservationType: domain.ObservationObserved,
	}}, nil
}

type admissionWriteChannel struct {
	incompleteShutdownChannel
	writes atomic.Int32
}

func (c *admissionWriteChannel) Write(context.Context, []byte) (int, error) {
	c.writes.Add(1)
	return 1, nil
}

type admissionWriteTransport struct{ channel *admissionWriteChannel }

func (t admissionWriteTransport) Dial(context.Context, domain.MachineRef, uint16, uint16, string) (guestssh.Channel, error) {
	return t.channel, nil
}

type admissionExpiryFixture struct {
	server      *Server
	backend     *gatedAdmissionBackend
	channel     *admissionWriteChannel
	token       string
	sessionPath string
}

func newAdmissionExpiryFixture(t *testing.T) admissionExpiryFixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	seedRetryShutdownTarget(t, root)
	backend := &gatedAdmissionBackend{}
	channel := &admissionWriteChannel{incompleteShutdownChannel: incompleteShutdownChannel{done: make(chan struct{})}}
	channel.complete.Store(true)
	keyProvider := &guestssh.MockKeyProvider{MachineConfig: &guestssh.MachineSSHConfig{
		Endpoint: "192.0.2.20:22", User: "synthetic", DefaultKeyAlias: "default",
		PinnedHostKeySHA256: "c3ludGhldGlj", ExternalEffectsContained: true,
		RollbackCheckpointID: "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
	}}
	server, err := NewServer(Config{StateDir: root, Backend: backend, Transport: admissionWriteTransport{channel}, KeyProvider: keyProvider})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := server.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	token, err := auth.ReadTokenFile(filepath.Join(root, "auth"), auth.TokenTypeOperator)
	if err != nil {
		t.Fatal(err)
	}
	fixture := admissionExpiryFixture{server: server, backend: backend, channel: channel, token: token}
	opened := fixture.serve(t.Context(), t, "/v1/sessions", SessionOpenRequest{
		Target: exactRetryVMID, Reason: "unmeasured session setup", IdempotencyKey: "subsecond-setup-open", TimeoutSeconds: 30,
	})
	var session SessionOpenResponse
	if opened.Code != http.StatusOK || json.Unmarshal(opened.Body.Bytes(), &session) != nil || session.Session.SessionID == "" {
		t.Fatalf("setup status=%d body=%s", opened.Code, opened.Body)
	}

	fixture.sessionPath = "/v1/sessions/" + session.Session.SessionID
	return fixture
}

func (f admissionExpiryFixture) serve(ctx context.Context, t *testing.T, path string, input any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+f.token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	f.server.httpServer.Handler.ServeHTTP(response, request)
	return response
}

func TestDaemonSessions_SubSecondAdmissionExpiryHasNoTransportEffect(t *testing.T) {
	fixture := newAdmissionExpiryFixture(t)
	parent := &gatedAdmissionDeadline{Context: context.Background(), done: make(chan struct{})}
	defer parent.expire()
	watchdog := time.AfterFunc(5*time.Second, parent.expire)
	defer watchdog.Stop()
	var gateCalls int
	fixture.backend.gate = func(ctx context.Context) {
		gateCalls++
		deadline, ok := ctx.Deadline()
		if !ok || !deadline.Equal(parent.deadline) || ctx.Err() != nil {
			t.Errorf("gate deadline=%v present=%v error=%v, want live inherited 250ms deadline %v", deadline, ok, ctx.Err(), parent.deadline)
		}
		parent.expire()
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("gate did not receive deadline cancellation")
		}
		if ctx.Err() != context.DeadlineExceeded {
			t.Errorf("gate cancellation=%v, want deadline exceeded", ctx.Err())
		}
	}
	response := fixture.serve(parent, t, fixture.sessionPath+"/write", SessionWriteRequest{
		Data: "x", Reason: "expire before transport", IdempotencyKey: "subsecond-expired-write", TimeoutMillis: 250,
	})
	var failure ErrorEnvelope
	if response.Code != http.StatusGatewayTimeout || json.Unmarshal(response.Body.Bytes(), &failure) != nil || failure.Error.Category != "timeout" {
		t.Fatalf("expired write status=%d body=%s", response.Code, response.Body)
	}
	if gateCalls != 1 || fixture.channel.writes.Load() != 0 {
		t.Fatalf("gate calls=%d writes=%d, want one admission expiry and zero transport effect", gateCalls, fixture.channel.writes.Load())
	}
}
