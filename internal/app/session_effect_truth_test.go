package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
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

type effectTruthTransport struct{ channel guestssh.Channel }

func (t effectTruthTransport) Dial(context.Context, domain.MachineRef, uint16, uint16, string) (guestssh.Channel, error) {
	return t.channel, nil
}

type acceptedControlCancelChannel struct {
	done   chan struct{}
	cancel context.CancelFunc
	calls  atomic.Int32
	once   sync.Once
}

func (c *acceptedControlCancelChannel) Read([]byte) (int, error) { <-c.done; return 0, io.EOF }
func (c *acceptedControlCancelChannel) Write(_ context.Context, data []byte) (int, error) {
	return len(data), nil
}
func (c *acceptedControlCancelChannel) SendControl(ctx context.Context, _ domain.ControlKey) (guestssh.ControlResult, error) {
	c.calls.Add(1)
	c.cancel()
	<-ctx.Done()
	return guestssh.ControlResult{AcceptedBytes: 1, EffectApplied: true}, ctx.Err()
}
func (c *acceptedControlCancelChannel) Resize(uint16, uint16) error { return nil }
func (c *acceptedControlCancelChannel) Close(context.Context) error {
	c.once.Do(func() { close(c.done) })
	return nil
}
func (c *acceptedControlCancelChannel) Wait() (int, error) { <-c.done; return 0, nil }

type zeroByteAppliedControlChannel struct {
	done  chan struct{}
	calls atomic.Int32
	once  sync.Once
}

func (c *zeroByteAppliedControlChannel) Read([]byte) (int, error) { <-c.done; return 0, io.EOF }
func (c *zeroByteAppliedControlChannel) Write(_ context.Context, data []byte) (int, error) {
	return len(data), nil
}
func (c *zeroByteAppliedControlChannel) SendControl(context.Context, domain.ControlKey) (guestssh.ControlResult, error) {
	c.calls.Add(1)
	return guestssh.ControlResult{EffectApplied: true}, nil
}
func (c *zeroByteAppliedControlChannel) Resize(uint16, uint16) error { return nil }
func (c *zeroByteAppliedControlChannel) Close(context.Context) error {
	c.once.Do(func() { close(c.done) })
	return nil
}
func (c *zeroByteAppliedControlChannel) Wait() (int, error) { <-c.done; return 0, nil }

type terminalCloseDeadlineChannel struct {
	done  chan struct{}
	calls atomic.Int32
	once  sync.Once
	last  guestssh.CloseOutcome
	mu    sync.Mutex
}

func (c *terminalCloseDeadlineChannel) Read([]byte) (int, error) { <-c.done; return 0, io.EOF }
func (c *terminalCloseDeadlineChannel) Write(_ context.Context, data []byte) (int, error) {
	return len(data), nil
}
func (c *terminalCloseDeadlineChannel) SendControl(context.Context, domain.ControlKey) (guestssh.ControlResult, error) {
	return guestssh.ControlResult{AcceptedBytes: 1, EffectApplied: true}, nil
}
func (c *terminalCloseDeadlineChannel) Resize(uint16, uint16) error { return nil }
func (c *terminalCloseDeadlineChannel) Close(context.Context) error {
	c.calls.Add(1)
	c.mu.Lock()
	c.last = guestssh.CloseOutcome{Complete: true, Err: context.DeadlineExceeded}
	c.mu.Unlock()
	c.once.Do(func() { close(c.done) })
	return context.DeadlineExceeded
}
func (c *terminalCloseDeadlineChannel) Wait() (int, error) { <-c.done; return 0, nil }
func (c *terminalCloseDeadlineChannel) LastCloseOutcome() guestssh.CloseOutcome {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

type effectTruthHarness struct {
	svc        *app.SessionService
	actor      domain.ActorContext
	opened     *domain.SessionObservation
	mutations  string
	checkpoint string
}

func newEffectTruthHarness(t *testing.T, channel guestssh.Channel) effectTruthHarness {
	t.Helper()
	sd, err := statedir.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	mgr := sessions.NewManager(sd.SessionsDir(), effectTruthTransport{channel: channel}, time.Now)
	svc := app.NewSessionService(
		mgr,
		diagnosticReversibleSafety{},
		nil,
		audit.NewStore(sd.AuditDir()),
		receipt.NewStore(sd.ReceiptsDir()),
		approval.NewStore(sd.ApprovalsDir()),
	)
	scopes := domain.NewScopeSet(domain.ScopeSessionOpen, domain.ScopeSessionRead, domain.ScopeSessionWrite, domain.ScopeSessionClose)
	actor, err := domain.NewActorContext("agent:effect-truth", "agent:effect-truth", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	opened, _, err := svc.OpenSession(context.Background(), app.SessionOpenParams{
		Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Caller: actor,
		Reason: "open effect truth session", IdempotencyKey: "effect-truth-open",
	})
	if err != nil {
		t.Fatal(err)
	}
	return effectTruthHarness{
		svc: svc, actor: actor, opened: opened,
		mutations:  filepath.Join(sd.SessionsDir(), "mutations"),
		checkpoint: "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
	}
}

func mutationReservationByKey(t *testing.T, dir, key string) sessions.MutationReservation {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		data, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		var reservation sessions.MutationReservation
		if err := json.Unmarshal(data, &reservation); err != nil {
			t.Fatal(err)
		}
		if reservation.IdempotencyKey == key {
			return reservation
		}
	}
	t.Fatalf("mutation reservation %q not found", key)
	return sessions.MutationReservation{}
}

func assertFailedEffectReceipt(t *testing.T, receipt *domain.Receipt, checkpoint, sessionID string) {
	t.Helper()
	if receipt == nil || receipt.Outcome.Status != domain.OutcomeFailed {
		t.Fatalf("receipt = %+v, want failed", receipt)
	}
	if receipt.RollbackRef != checkpoint {
		t.Fatalf("rollback_ref = %q, want %q", receipt.RollbackRef, checkpoint)
	}
	if len(receipt.EvidenceRefs) != 1 || receipt.EvidenceRefs[0] != sessionID {
		t.Fatalf("evidence_refs = %v, want session %q", receipt.EvidenceRefs, sessionID)
	}
}

func TestControlAcceptedBytesThenCancellationFinalizesEffectTruth(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	channel := &acceptedControlCancelChannel{done: make(chan struct{}), cancel: cancel}
	h := newEffectTruthHarness(t, channel)
	params := app.SessionControlParams{
		SessionID: h.opened.ID, Caller: h.actor, Key: domain.ControlKeyCtrlC,
		Reason: "control accepted before cancellation", IdempotencyKey: "effect-truth-control", Timeout: time.Second,
	}
	firstReceipt, firstErr := h.svc.ControlSession(parent, params)
	if !errors.Is(firstErr, context.Canceled) {
		t.Fatalf("control error = %v, want context canceled", firstErr)
	}
	assertFailedEffectReceipt(t, firstReceipt, h.checkpoint, string(h.opened.ID))
	reservation := mutationReservationByKey(t, h.mutations, params.IdempotencyKey)
	if reservation.Result.BytesWritten != 1 || reservation.Result.EffectApplied == nil || !*reservation.Result.EffectApplied {
		t.Fatalf("durable control result = %+v, want one accepted byte and applied effect", reservation.Result)
	}

	retryReceipt, retryErr := h.svc.ControlSession(context.Background(), params)
	if retryErr == nil || retryReceipt == nil || retryReceipt.ReceiptID != firstReceipt.ReceiptID {
		t.Fatalf("exact retry = receipt %+v err %v, want identical failed durable truth", retryReceipt, retryErr)
	}
	if got := channel.calls.Load(); got != 1 {
		t.Fatalf("control calls after exact retry = %d, want 1", got)
	}
}

func TestControlZeroBytesAppliedIsDurableAndNotReplayed(t *testing.T) {
	channel := &zeroByteAppliedControlChannel{done: make(chan struct{})}
	h := newEffectTruthHarness(t, channel)
	params := app.SessionControlParams{
		SessionID: h.opened.ID, Caller: h.actor, Key: domain.ControlKeyCtrlC,
		Reason: "control applied without payload bytes", IdempotencyKey: "effect-truth-zero-byte-control", Timeout: time.Second,
	}
	firstReceipt, firstErr := h.svc.ControlSession(context.Background(), params)
	if firstErr != nil || firstReceipt == nil || firstReceipt.Outcome.Status != domain.OutcomeSuccess {
		t.Fatalf("control receipt = %+v err %v, want success", firstReceipt, firstErr)
	}
	reservation := mutationReservationByKey(t, h.mutations, params.IdempotencyKey)
	if reservation.Result.BytesWritten != 0 || reservation.Result.EffectApplied == nil || !*reservation.Result.EffectApplied {
		t.Fatalf("durable control result = %+v, want zero accepted bytes and applied effect", reservation.Result)
	}

	retryReceipt, retryErr := h.svc.ControlSession(context.Background(), params)
	if retryErr != nil || retryReceipt == nil || retryReceipt.ReceiptID != firstReceipt.ReceiptID {
		t.Fatalf("exact retry = receipt %+v err %v, want identical success", retryReceipt, retryErr)
	}
	if got := channel.calls.Load(); got != 1 {
		t.Fatalf("control calls after exact retry = %d, want 1", got)
	}
}

func TestTerminalCloseDeadlineFinalizesEffectTruth(t *testing.T) {
	channel := &terminalCloseDeadlineChannel{done: make(chan struct{})}
	h := newEffectTruthHarness(t, channel)
	params := app.SessionCloseParams{
		SessionID: h.opened.ID, Caller: h.actor, Reason: "terminal cleanup reports deadline",
		IdempotencyKey: "effect-truth-close", Timeout: time.Second,
	}
	firstObservation, firstReceipt, firstErr := h.svc.CloseSession(context.Background(), params)
	if !errors.Is(firstErr, context.DeadlineExceeded) || firstObservation == nil || firstObservation.State != domain.SessionStateFailed {
		t.Fatalf("close = observation %+v receipt %+v err %v, want terminal failed deadline", firstObservation, firstReceipt, firstErr)
	}
	assertFailedEffectReceipt(t, firstReceipt, h.checkpoint, string(h.opened.ID))
	reservation := mutationReservationByKey(t, h.mutations, params.IdempotencyKey)
	if reservation.Result.EffectApplied == nil || !*reservation.Result.EffectApplied || reservation.Result.Observation == nil || !reservation.Result.Observation.State.IsTerminal() {
		t.Fatalf("durable close result = %+v, want applied terminal effect", reservation.Result)
	}

	retryObservation, retryReceipt, retryErr := h.svc.CloseSession(context.Background(), params)
	if retryErr == nil || retryReceipt == nil || retryReceipt.ReceiptID != firstReceipt.ReceiptID || retryObservation == nil || retryObservation.State != firstObservation.State {
		t.Fatalf("exact retry = observation %+v receipt %+v err %v, want identical failed durable truth", retryObservation, retryReceipt, retryErr)
	}
	if got := channel.calls.Load(); got != 1 {
		t.Fatalf("close calls after exact retry = %d, want 1", got)
	}
}
