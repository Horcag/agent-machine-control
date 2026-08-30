package daemon

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

type retryShutdownBackend struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *retryShutdownBackend) Doctor(context.Context) (app.DoctorReport, error) {
	return app.DoctorReport{}, nil
}
func (b *retryShutdownBackend) ListMachines(context.Context) ([]domain.MachineObservation, error) {
	return nil, nil
}
func (b *retryShutdownBackend) InspectMachine(context.Context, string) (domain.MachineObservation, error) {
	return domain.MachineObservation{}, nil
}
func (b *retryShutdownBackend) Capabilities(context.Context, string) (domain.CapabilitySet, error) {
	return domain.NewCapabilitySet(domain.CapabilityMachineStart), nil
}
func (b *retryShutdownBackend) StartMachine(_ context.Context, target string) (domain.MachineObservation, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return domain.MachineObservation{ID: target, State: domain.MachineStateRunning}, nil
}
func (b *retryShutdownBackend) StopMachine(context.Context, string, string) (domain.MachineObservation, error) {
	return domain.MachineObservation{}, nil
}
func (b *retryShutdownBackend) ListCheckpoints(_ context.Context, target string) ([]domain.CheckpointObservation, error) {
	now := time.Now().UTC()
	return []domain.CheckpointObservation{{
		ID: "e4a523d4-6b99-4d62-a5e2-4752c0f20001", Name: "shutdown-safe", VMID: target,
		CreatedAt: now, ObservedAt: now, ObservationType: domain.ObservationObserved,
	}}, nil
}
func (b *retryShutdownBackend) CreateCheckpoint(context.Context, string, string) (domain.CheckpointObservation, error) {
	return domain.CheckpointObservation{}, nil
}
func (b *retryShutdownBackend) RestoreCheckpoint(context.Context, string, string) (domain.MachineObservation, error) {
	return domain.MachineObservation{}, nil
}

func retryShutdownOperation(t *testing.T) domain.Operation {
	t.Helper()
	scopes := domain.NewScopeSet(domain.ScopeMachineWrite)
	actor, err := domain.NewActorContext("operator:shutdown-retry", "operator:shutdown-retry", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	return domain.Operation{
		Kind: "machine.start", Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Actor: actor,
		Reason: "exercise retryable shutdown ownership", Deadline: time.Now().Add(time.Minute),
		IdempotencyKey: "shutdown-retry-operation", RequiredCapability: domain.CapabilityMachineStart,
		RequiredScopes: []string{domain.ScopeMachineWrite}, Classification: domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}
}

func assertShutdownOwnershipPresent(t *testing.T, server *Server, stateDir string) {
	t.Helper()
	record, err := ReadEndpointFile(filepath.Join(stateDir, "daemon"))
	if err != nil {
		t.Fatalf("read retained endpoint: %v", err)
	}
	if record.Endpoint != server.Endpoint() || record.PID != server.PID() || record.RuntimeID != server.runtimeID {
		t.Fatalf("retained endpoint record = %+v, want exact server identity", record)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "daemon", "singleton.lock", "owner.json")); err != nil {
		t.Fatalf("retained singleton owner: %v", err)
	}
}

func TestServerShutdownRetainsOwnershipUntilBlockedOperationDrains(t *testing.T) {
	stateDir := t.TempDir()
	backend := &retryShutdownBackend{started: make(chan struct{}), release: make(chan struct{})}
	server, err := NewServer(Config{StateDir: stateDir, ListenAddr: "127.0.0.1:0", Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-backend.release:
		default:
			close(backend.release)
		}
		_ = server.Shutdown(context.Background())
	})
	if _, _, err := server.opMgr.Submit(context.Background(), retryShutdownOperation(t), time.Minute); err != nil {
		t.Fatal(err)
	}
	select {
	case <-backend.started:
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = server.Shutdown(ctx)
	cancel()
	if !errors.Is(err, ErrShutdownIncomplete) {
		t.Fatalf("Shutdown error = %v, want ErrShutdownIncomplete", err)
	}
	assertShutdownOwnershipPresent(t, server, stateDir)
	if second, err := NewServer(Config{StateDir: stateDir, ListenAddr: "127.0.0.1:0", Backend: backend}); err == nil {
		_ = second.Shutdown(context.Background())
		t.Fatal("second daemon acquired ownership while cleanup remained live")
	}

	close(backend.release)
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	err = server.Shutdown(ctx)
	cancel()
	if err != nil {
		t.Fatalf("retry Shutdown failed: %v", err)
	}
	verifyShutdownFilesRemovedInternal(t, stateDir)
}

type incompleteShutdownTransport struct{ channel *incompleteShutdownChannel }

func (t *incompleteShutdownTransport) Dial(context.Context, domain.MachineRef, uint16, uint16, string) (guestssh.Channel, error) {
	return t.channel, nil
}

type incompleteShutdownChannel struct {
	complete atomic.Bool
	done     chan struct{}
	once     sync.Once
}

func (c *incompleteShutdownChannel) Read([]byte) (int, error)                   { <-c.done; return 0, io.EOF }
func (c *incompleteShutdownChannel) Write(context.Context, []byte) (int, error) { return 0, nil }
func (c *incompleteShutdownChannel) SendControl(context.Context, domain.ControlKey) (guestssh.ControlResult, error) {
	return guestssh.ControlResult{}, nil
}
func (c *incompleteShutdownChannel) Resize(uint16, uint16) error { return nil }
func (c *incompleteShutdownChannel) Close(ctx context.Context) error {
	if c.complete.Load() {
		c.once.Do(func() { close(c.done) })
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}
func (c *incompleteShutdownChannel) LastCloseOutcome() guestssh.CloseOutcome {
	return guestssh.CloseOutcome{Complete: c.complete.Load()}
}
func (c *incompleteShutdownChannel) Wait() (int, error) { <-c.done; return 0, nil }

func TestServerShutdownRetainsOwnershipForIncompleteSessionTransport(t *testing.T) {
	stateDir := t.TempDir()
	channel := &incompleteShutdownChannel{done: make(chan struct{})}
	server, err := NewServer(Config{
		StateDir: stateDir, ListenAddr: "127.0.0.1:0", Backend: &retryShutdownBackend{},
		Transport: &incompleteShutdownTransport{channel: channel},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		channel.complete.Store(true)
		channel.once.Do(func() { close(channel.done) })
		_ = server.Shutdown(context.Background())
	})
	scopes := domain.NewScopeSet(domain.ScopeSessionOpen, domain.ScopeSessionAdmin)
	actor, err := domain.NewActorContext("operator:session-shutdown", "operator:session-shutdown", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	operation := domain.Operation{
		Kind: "session.open", Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Actor: actor,
		Reason: "exercise incomplete transport shutdown", Deadline: time.Now().Add(time.Minute),
		IdempotencyKey: "shutdown-incomplete-session", RequiredCapability: domain.CapabilitySessionOpen,
		RequiredScopes: []string{domain.ScopeSessionOpen}, Classification: domain.ClassObserve,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}
	if _, err := server.sessionMgr.Open(context.Background(), operation, 80, 24, "xterm-256color"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = server.Shutdown(ctx)
	cancel()
	if !errors.Is(err, ErrShutdownIncomplete) {
		t.Fatalf("Shutdown error = %v, want ErrShutdownIncomplete", err)
	}
	assertShutdownOwnershipPresent(t, server, stateDir)

	channel.complete.Store(true)
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	err = server.Shutdown(ctx)
	cancel()
	if err != nil {
		t.Fatalf("retry Shutdown failed: %v", err)
	}
	verifyShutdownFilesRemovedInternal(t, stateDir)
}

func TestServerShutdownContinuesTeardownAfterTerminalFinalizationError(t *testing.T) {
	stateDir := t.TempDir()
	backend := &retryShutdownBackend{started: make(chan struct{}), release: make(chan struct{})}
	server, err := NewServer(Config{StateDir: stateDir, ListenAddr: "127.0.0.1:0", Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.opMgr.Submit(context.Background(), retryShutdownOperation(t), time.Minute); err != nil {
		t.Fatal(err)
	}
	select {
	case <-backend.started:
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
	}
	if err := server.eventHub.Close(); err != nil {
		t.Fatal(err)
	}
	close(backend.release)
	deadline := time.Now().Add(time.Second)
	for !server.opMgr.Drained() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !server.opMgr.Drained() {
		t.Fatal("operation did not drain")
	}

	err = server.Shutdown(context.Background())
	if err == nil || errors.Is(err, ErrShutdownIncomplete) {
		t.Fatalf("Shutdown error = %v, want post-drain finalization error", err)
	}
	verifyShutdownFilesRemovedInternal(t, stateDir)
}

func TestServerShutdownHTTPFailureStillRemovesOwnershipAndRepeatedShutdownIsSafe(t *testing.T) {
	stateDir := t.TempDir()
	server, err := NewServer(Config{StateDir: stateDir, ListenAddr: "127.0.0.1:0", Backend: &retryShutdownBackend{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	httpErr := errors.New("synthetic http shutdown failure")
	originalClose := server.closeHTTP
	var forcedCloseCalls atomic.Int32
	server.shutdownHTTP = func(context.Context) error { return httpErr }
	server.closeHTTP = func() error {
		forcedCloseCalls.Add(1)
		return originalClose()
	}

	err = server.Shutdown(context.Background())
	if !errors.Is(err, httpErr) || errors.Is(err, ErrShutdownIncomplete) {
		t.Fatalf("Shutdown error = %v, want HTTP finalization error only", err)
	}
	if forcedCloseCalls.Load() != 1 {
		t.Fatalf("forced HTTP close calls = %d, want one", forcedCloseCalls.Load())
	}
	verifyShutdownFilesRemovedInternal(t, stateDir)

	server.shutdownHTTP = nil
	server.closeHTTP = nil
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatalf("repeated Shutdown failed: %v", err)
	}
}

func verifyShutdownFilesRemovedInternal(t *testing.T, stateDir string) {
	t.Helper()
	for _, path := range []string{
		filepath.Join(stateDir, "daemon", "endpoint.json"),
		filepath.Join(stateDir, "daemon", "singleton.lock", "owner.json"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("shutdown ownership path %q remains: %v", path, err)
		}
	}
}

var _ app.Backend = (*retryShutdownBackend)(nil)
var _ guestssh.Transport = (*incompleteShutdownTransport)(nil)
var _ guestssh.Channel = (*incompleteShutdownChannel)(nil)
