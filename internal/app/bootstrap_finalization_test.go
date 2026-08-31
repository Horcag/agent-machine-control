package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
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
	if remaining := time.Until(finalizationDeadline); remaining <= 5*time.Second || remaining > bootstrapPostEffectFinalizationGrace {
		t.Fatalf("post-effect finalization deadline remaining = %s, want bounded grace greater than legacy five seconds", remaining)
	}

	if err := service.auditStore.VerifyTerminalOutcome(requireSingleBootstrapTerminalReceipt(t, service, result.ReceiptID)); err != nil {
		t.Fatalf("VerifyTerminalOutcome() error = %v", err)
	}
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
