package operations_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/events"
	"github.com/Horcag/agent-machine-control/internal/operations"
)

func TestManagerCapabilitiesDeadlineDrainsWithoutProviderEffect(t *testing.T) {
	runManagerPreProviderDeadline(t, "deadline-capabilities", func(backend *mockBackend, seamCalls *atomic.Int32) {
		backend.capabilitiesFn = func(ctx context.Context, _ string) (domain.CapabilitySet, error) {
			seamCalls.Add(1)
			<-ctx.Done()
			return nil, ctx.Err()
		}
	})
}

func TestManagerRollbackDeadlineDrainsWithoutProviderEffect(t *testing.T) {
	runManagerPreProviderDeadline(t, "deadline-rollback", func(backend *mockBackend, seamCalls *atomic.Int32) {
		backend.listCheckpointsFn = func(ctx context.Context, _ string) ([]domain.CheckpointObservation, error) {
			seamCalls.Add(1)
			<-ctx.Done()
			return nil, ctx.Err()
		}
	})
}

func runManagerPreProviderDeadline(t *testing.T, key string, install func(*mockBackend, *atomic.Int32)) {
	t.Helper()
	backend := &mockBackend{}
	var seamCalls atomic.Int32
	install(backend, &seamCalls)
	testNow := time.Now().UTC()
	manager, hub, _ := setupTestManager(t, backend, operations.WithClock(func() time.Time { return testNow }))
	operation := deadlineTestOperation(key, testNow.Add(time.Second))

	started := time.Now()
	record, _, err := manager.Submit(context.Background(), operation, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	final := waitForTerminalOperation(t, manager, hub, record.ID, operation.Actor)
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("deadline completion took %s", elapsed)
	}
	if final.State != domain.OpStateFailed || final.ErrorCategory != "timeout" {
		t.Fatalf("terminal state = %s category = %q, want failed timeout", final.State, final.ErrorCategory)
	}
	if backend.startCount.Load() != 0 {
		t.Fatalf("provider calls = %d, want zero", backend.startCount.Load())
	}
	if seamCalls.Load() != 1 {
		t.Fatalf("blocking seam calls = %d, want one", seamCalls.Load())
	}
	retry, existing, err := manager.Submit(context.Background(), operation, 5*time.Second)
	if err != nil || !existing || retry.ID != record.ID {
		t.Fatalf("idempotent retry = (%+v, %v, %v), want original terminal operation", retry, existing, err)
	}
	if backend.startCount.Load() != 0 {
		t.Fatalf("retry provider calls = %d, want zero", backend.startCount.Load())
	}
	assertManagerShutdownDrains(t, manager)
}

func TestManagerOperatorCancellationRemainsDistinctFromDeadline(t *testing.T) {
	entered := make(chan struct{})
	backend := &mockBackend{
		capabilitiesFn: func(ctx context.Context, _ string) (domain.CapabilitySet, error) {
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	testNow := time.Now().UTC()
	manager, hub, _ := setupTestManager(t, backend, operations.WithClock(func() time.Time { return testNow }))
	operation := deadlineTestOperation("operator-cancel", testNow.Add(5*time.Second))
	record, _, err := manager.Submit(context.Background(), operation, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := manager.Cancel(record.ID, operation.Actor, "operator cancellation"); err != nil {
		t.Fatal(err)
	}

	final := waitForTerminalOperation(t, manager, hub, record.ID, operation.Actor)
	if final.State != domain.OpStateCancelled || final.ErrorCategory != "cancelled" {
		t.Fatalf("terminal state = %s category = %q, want cancelled", final.State, final.ErrorCategory)
	}
	assertManagerShutdownDrains(t, manager)
	if backend.startCount.Load() != 0 {
		t.Fatalf("provider calls = %d, want zero", backend.startCount.Load())
	}
}

func assertManagerShutdownDrains(t *testing.T, manager *operations.Manager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatalf("manager shutdown: %v", err)
	}
	if !manager.Drained() {
		t.Fatal("manager retained live capacity after terminal operation")
	}
}

func deadlineTestOperation(key string, deadline time.Time) domain.Operation {
	scopes := domain.NewScopeSet("machine:write", "operation:cancel", "audit:read")
	actor, _ := domain.NewActorContext("operator:deadline-test", "operator:deadline-test", scopes, scopes)
	return domain.Operation{
		Kind: "machine.start", Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Actor: actor,
		Reason: "verify end-to-end deadline", Deadline: deadline, IdempotencyKey: key,
		RequiredCapability: domain.CapabilityMachineStart, RequiredScopes: []string{domain.ScopeMachineWrite},
		Classification: domain.ClassReversibleMutation, EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}
}

func waitForTerminalOperation(t *testing.T, manager *operations.Manager, hub *events.Hub, operationID string, actor domain.ActorContext) *domain.OperationRecord {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	events, unsubscribe, err := hub.Subscribe(ctx, operationID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("event stream closed before terminal operation")
			}
			if event.State.IsTerminal() {
				final, err := manager.Get(operationID, actor)
				if err != nil {
					t.Fatal(err)
				}
				return final
			}
		case <-ctx.Done():
			t.Fatalf("terminal operation wait: %v", ctx.Err())
		}
	}
}
