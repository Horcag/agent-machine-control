package app

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func TestBootstrapEnsureFinalizesAfterCallerCancellationWithinBoundedGrace(t *testing.T) {
	adapter := newFakeBootstrapAdapter()
	adapter.observation = BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}
	callerCtx, cancelCaller := context.WithCancel(context.Background())
	t.Cleanup(cancelCaller)

	var finalizationContextObserved bool
	var finalizationContextErr error
	var finalizationDeadline time.Time
	var finalizationDeadlineSet bool
	adapter.onInspectContext = func(ctx context.Context, calls int) {
		if calls == 3 {
			finalizationContextObserved = true
			finalizationContextErr = ctx.Err()
			finalizationDeadline, finalizationDeadlineSet = ctx.Deadline()
		}
	}
	daemon := &fakeBootstrapDaemon{
		healthy: true,
		onHealthCheck: func(calls int) {
			if calls == 1 {
				cancelCaller()
			}
		},
	}
	service := newTestBootstrapService(t, adapter, daemon)

	result, err := service.Ensure(callerCtx, BootstrapMutationRequest{
		StateDir: t.TempDir(), Reason: "finalize healthy bootstrap", IdempotencyKey: "finalization-grace",
		Deadline: time.Now().Add(time.Minute),
	})
	if err != nil || result.Status != BootstrapHealthy || result.ReceiptID == "" {
		t.Fatalf("Ensure() = %#v, %v; want healthy terminal receipt", result, err)
	}
	if err := callerCtx.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("caller context error = %v, want canceled", err)
	}
	if !finalizationContextObserved || finalizationContextErr != nil {
		t.Fatalf("post-effect finalization context observed=%t error=%v, want active independent context", finalizationContextObserved, finalizationContextErr)
	}
	if !finalizationDeadlineSet {
		t.Fatal("post-effect finalization context has no deadline")
	}
	if remaining := time.Until(finalizationDeadline); remaining <= 5*time.Second || remaining > bootstrapPostEffectObservationGrace {
		t.Fatalf("post-effect finalization deadline remaining = %s, want bounded grace greater than legacy five seconds", remaining)
	}

	if err := service.auditStore.VerifyTerminalOutcome(requireSingleBootstrapTerminalReceipt(t, service, result.ReceiptID)); err != nil {
		t.Fatalf("VerifyTerminalOutcome() error = %v", err)
	}
}

func TestBootstrapEnsureObservationTimeoutFinalizesDurablyAndReplays(t *testing.T) {
	adapter := newFakeBootstrapAdapter()
	callerCtx, cancelCaller := context.WithCancel(context.Background())
	t.Cleanup(cancelCaller)

	timeout := &postEffectObservationTimeout{adapter: adapter, cancelCaller: cancelCaller}
	adapter.onInspectContext = timeout.block

	service := newTestBootstrapService(t, adapter, &fakeBootstrapDaemon{becomesHealthyAfter: 1})
	service.observationGrace = 10 * time.Millisecond
	service.evidenceGrace = 250 * time.Millisecond

	contexts := &durableEvidenceContexts{caller: callerCtx}
	service.receiptStore = receipt.NewStore(receiptStoreDir(t), receipt.WithEnsureHook(func(ctx context.Context, _ domain.Receipt) error {
		contexts.receiptActive = contexts.active(ctx)
		return nil
	}))
	service.auditStore = audit.NewStore(auditStoreDir(t), audit.WithEnsureHook(func(ctx context.Context, event audit.Event) error {
		if event.EventType == audit.EventTerminalOutcome {
			contexts.auditActive = contexts.active(ctx)
		}
		return nil
	}))

	req := BootstrapMutationRequest{
		StateDir: t.TempDir(), Reason: "persist after inspect timeout", IdempotencyKey: "observation-timeout",
		Deadline: time.Now().Add(time.Minute),
	}
	first, err := service.Ensure(callerCtx, req)
	assertTimedOutInferredResult(t, first, err)
	assertEffectAndObservationTimeout(t, adapter, timeout)
	contexts.assertActive(t)

	record := requireSingleBootstrapTerminalReceipt(t, service, first.ReceiptID)
	assertInferredTerminalReceipt(t, record)
	if err := service.auditStore.VerifyTerminalOutcome(record); err != nil {
		t.Fatalf("VerifyTerminalOutcome() error = %v", err)
	}

	second, retryErr := service.Ensure(context.Background(), req)
	assertReplayedFailureWithoutEffects(t, first, second, retryErr, adapter)
	requireSingleBootstrapTerminalReceipt(t, service, first.ReceiptID)
	if err := service.auditStore.VerifyTerminalOutcome(record); err != nil {
		t.Fatalf("VerifyTerminalOutcome() after replay error = %v", err)
	}
}

type postEffectObservationTimeout struct {
	adapter      *fakeBootstrapAdapter
	cancelCaller context.CancelFunc
	expired      bool
}

func (t *postEffectObservationTimeout) block(ctx context.Context, calls int) {
	if calls != 3 {
		return
	}
	t.cancelCaller()
	<-ctx.Done()
	t.expired = errors.Is(ctx.Err(), context.DeadlineExceeded)
	t.adapter.inspectErr = ctx.Err()
}

type durableEvidenceContexts struct {
	caller                     context.Context
	receiptActive, auditActive bool
}

func (c *durableEvidenceContexts) active(ctx context.Context) bool {
	return ctx.Err() == nil && c.caller.Err() != nil
}

func (c *durableEvidenceContexts) assertActive(t *testing.T) {
	t.Helper()
	if !c.receiptActive || !c.auditActive {
		t.Fatalf("durable contexts receipt=%t audit=%t, want fresh active contexts after caller and observation expiry", c.receiptActive, c.auditActive)
	}
}

func assertTimedOutInferredResult(t *testing.T, result BootstrapResult, err error) {
	t.Helper()
	if !errors.Is(err, context.DeadlineExceeded) || result.ReceiptID == "" || result.Status != "" {
		t.Fatalf("Ensure() = %#v, %v; want timed-out inferred terminal receipt", result, err)
	}
}

func assertEffectAndObservationTimeout(t *testing.T, adapter *fakeBootstrapAdapter, timeout *postEffectObservationTimeout) {
	t.Helper()
	if adapter.installCalls != 1 || adapter.startCalls != 1 || !timeout.expired {
		t.Fatalf("effect calls install=%d start=%d observationExpired=%t", adapter.installCalls, adapter.startCalls, timeout.expired)
	}
}

func assertInferredTerminalReceipt(t *testing.T, record domain.Receipt) {
	t.Helper()
	if record.Outcome.Status != domain.OutcomeAborted || record.ObservationType != domain.ObservationInferred {
		t.Fatalf("terminal receipt = %#v, want aborted inferred outcome", record)
	}
}

func assertReplayedFailureWithoutEffects(t *testing.T, first, second BootstrapResult, err error, adapter *fakeBootstrapAdapter) {
	t.Helper()
	if !errors.Is(err, ErrBootstrapPriorFailed) || !second.Replayed || second.ReceiptID != first.ReceiptID {
		t.Fatalf("exact retry = %#v, %v; want replayed prior failure", second, err)
	}
	if adapter.installCalls != 1 || adapter.startCalls != 1 {
		t.Fatalf("exact retry repeated effects install=%d start=%d", adapter.installCalls, adapter.startCalls)
	}
}

func receiptStoreDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(root+"/receipts", 0700); err != nil {
		t.Fatal(err)
	}
	return root + "/receipts"
}

func auditStoreDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(root+"/audit", 0700); err != nil {
		t.Fatal(err)
	}
	return root + "/audit"
}

func requireSingleBootstrapTerminalReceipt(t *testing.T, service *BootstrapService, receiptID string) domain.Receipt {
	t.Helper()
	receipts, err := service.receiptStore.List(10, "")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(receipts) != 1 {
		t.Fatalf("terminal receipts = %#v, want exactly one receipt", receipts)
	}
	if got := string(receipts[0].ReceiptID); got != receiptID {
		t.Fatalf("terminal receipt ID = %q, want %q", got, receiptID)
	}
	return receipts[0]
}
