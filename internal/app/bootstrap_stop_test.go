package app

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestBootstrapServiceStopUsesGracefulThenExactTaskFallback(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	adapter.observation = BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}
	daemon := &fakeBootstrapDaemon{healthy: true, stopLeavesHealthy: true}
	service := newTestBootstrapService(t, adapter, daemon)
	service.poll = pollBootstrapChecks(1)

	result, err := service.Stop(context.Background(), BootstrapMutationRequest{
		StateDir:       testStateDir(t),
		Reason:         "maintenance",
		IdempotencyKey: "stop-with-fallback",
		Deadline:       time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if result.Status != BootstrapStopped {
		t.Fatalf("Stop() status = %q, want stopped", result.Status)
	}
	if daemon.stopCalls != 1 || adapter.stopCalls != 1 {
		t.Fatalf("stop order effects graceful=%d fallback=%d", daemon.stopCalls, adapter.stopCalls)
	}
}

func TestBootstrapServiceStopWaitsForGracefulDrainWithoutTaskFallback(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	adapter.observation = BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}
	daemon := &fakeBootstrapDaemon{healthy: true, stopLeavesHealthy: true}
	daemon.onHealthCheck = func(check int) {
		if check == 3 {
			adapter.observation = BootstrapObservation{State: BootstrapStopped, Exact: true}
		}
	}
	service := newTestBootstrapService(t, adapter, daemon)
	service.poll = pollBootstrapChecks(3)

	result, err := service.Stop(context.Background(), BootstrapMutationRequest{
		StateDir: testStateDir(t), Reason: "graceful maintenance", IdempotencyKey: "stop-graceful",
		Deadline: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if result.Status != BootstrapStopped || daemon.stopCalls != 1 || adapter.stopCalls != 0 {
		t.Fatalf("Stop() = %#v graceful=%d fallback=%d", result, daemon.stopCalls, adapter.stopCalls)
	}
	if daemon.healthChecks < 4 {
		t.Fatalf("graceful drain used only %d health checks, want delayed polling", daemon.healthChecks)
	}
}

func TestBootstrapServiceStopFallsBackWhenEndpointUnavailable(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	adapter.observation = BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}
	service := newTestBootstrapService(t, adapter, &fakeBootstrapDaemon{})

	result, err := service.Stop(context.Background(), BootstrapMutationRequest{
		StateDir: testStateDir(t), Reason: "recover unavailable endpoint", IdempotencyKey: "stop-unavailable",
		Deadline: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if result.Status != BootstrapStopped || adapter.stopCalls != 1 {
		t.Fatalf("Stop() = %#v fallback=%d", result, adapter.stopCalls)
	}
}

func TestBootstrapServiceStopUsesTypedReleaseToRecoverStrictEndpointDrift(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	adapter.observation = BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}
	daemon := &fakeBootstrapDaemon{
		healthErr:     ErrBootstrapDrift,
		releaseStates: []BootstrapDaemonReleaseState{BootstrapDaemonEndpointUnavailable, BootstrapDaemonReleased},
	}
	daemon.onReleaseCheck = func(check int) {
		if check == 1 {
			daemon.healthErr = nil
		}
	}
	service := newTestBootstrapService(t, adapter, daemon)

	result, err := service.Stop(context.Background(), BootstrapMutationRequest{
		StateDir: testStateDir(t), Reason: "recover strict unavailable endpoint", IdempotencyKey: "stop-strict-unavailable",
		Deadline: time.Now().Add(time.Minute),
	})
	if err != nil || result.Status != BootstrapStopped {
		t.Fatalf("Stop() = %#v, %v", result, err)
	}
	if daemon.stopCalls != 0 || adapter.stopCalls != 1 || !result.TaskStopApplied {
		t.Fatalf("effects graceful=%d task=%d result=%#v", daemon.stopCalls, adapter.stopCalls, result)
	}
}

func TestBootstrapServiceStopUsesRecoveredTypedHealthAfterStrictDrift(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	adapter.observation = BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}
	daemon := &fakeBootstrapDaemon{
		healthErr:     ErrBootstrapDrift,
		releaseStates: []BootstrapDaemonReleaseState{BootstrapDaemonHealthy, BootstrapDaemonReleased},
	}
	daemon.onReleaseCheck = func(check int) {
		if check == 1 {
			daemon.healthErr = nil
		}
		if check == 2 {
			adapter.observation = BootstrapObservation{State: BootstrapStopped, Exact: true}
		}
	}
	service := newTestBootstrapService(t, adapter, daemon)

	result, err := service.Stop(context.Background(), BootstrapMutationRequest{
		StateDir: testStateDir(t), Reason: "stop recovered owned endpoint", IdempotencyKey: "stop-recovered-owned-endpoint",
		Deadline: time.Now().Add(time.Minute),
	})
	if err != nil || result.Status != BootstrapStopped {
		t.Fatalf("Stop() = %#v, %v", result, err)
	}
	if daemon.stopCalls != 1 || adapter.stopCalls != 0 || result.TaskStopApplied {
		t.Fatalf("effects graceful=%d task=%d result=%#v", daemon.stopCalls, adapter.stopCalls, result)
	}
}

func TestBootstrapServiceStopFallsBackWhenEndpointDisappearsDuringRequest(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	adapter.observation = BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}
	daemon := &fakeBootstrapDaemon{healthy: true, stopErr: ErrBootstrapEndpointUnavailable}
	service := newTestBootstrapService(t, adapter, daemon)
	service.poll = pollBootstrapChecks(1)

	result, err := service.Stop(context.Background(), BootstrapMutationRequest{
		StateDir: testStateDir(t), Reason: "endpoint race", IdempotencyKey: "stop-endpoint-race",
		Deadline: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if result.Status != BootstrapStopped || daemon.stopCalls != 1 || adapter.stopCalls != 1 {
		t.Fatalf("Stop() = %#v endpoint=%d fallback=%d", result, daemon.stopCalls, adapter.stopCalls)
	}
	if daemon.releaseChecks != 1 {
		t.Fatalf("post-fallback release checks=%d, want 1 without grace polling", daemon.releaseChecks)
	}
}

func TestBootstrapServiceStopFallsBackAfterAcknowledgedUnavailableDrain(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	adapter.observation = BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}
	daemon := &fakeBootstrapDaemon{
		healthy: true,
		releaseStates: []BootstrapDaemonReleaseState{
			BootstrapDaemonEndpointUnavailable,
			BootstrapDaemonShutdownPending,
			BootstrapDaemonReleased,
		},
	}
	service := newTestBootstrapService(t, adapter, daemon)
	service.poll = pollBootstrapChecks(2)

	result, err := service.Stop(context.Background(), BootstrapMutationRequest{
		StateDir: testStateDir(t), Reason: "fallback after acknowledged drain", IdempotencyKey: "stop-acked-drain",
		Deadline: time.Now().Add(time.Minute),
	})
	if err != nil || result.Status != BootstrapStopped {
		t.Fatalf("Stop() = %#v, %v", result, err)
	}
	if daemon.stopCalls != 1 || adapter.stopCalls != 1 || !result.TaskStopApplied {
		t.Fatalf("effects graceful=%d task=%d result=%#v", daemon.stopCalls, adapter.stopCalls, result)
	}
}

func TestBootstrapServiceStopBlocksTaskDriftAtFallbackBoundary(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	adapter.observation = BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}
	adapter.onInspect = func(call int) {
		if call == 3 {
			adapter.observation = BootstrapObservation{State: BootstrapDrift, Reason: BootstrapReasonTaskMismatch}
		}
	}
	daemon := &fakeBootstrapDaemon{
		healthy:       true,
		releaseStates: []BootstrapDaemonReleaseState{BootstrapDaemonShutdownPending},
	}
	service := newTestBootstrapService(t, adapter, daemon)
	service.poll = pollBootstrapChecks(1)

	_, err := service.Stop(context.Background(), BootstrapMutationRequest{
		StateDir: testStateDir(t), Reason: "reject fallback task drift", IdempotencyKey: "stop-fallback-task-drift",
		Deadline: time.Now().Add(time.Minute),
	})
	if !errors.Is(err, ErrBootstrapDrift) || adapter.stopCalls != 0 {
		t.Fatalf("Stop() error=%v task stops=%d", err, adapter.stopCalls)
	}
}

func TestBootstrapStopFailsWhenExactTaskRemainsRunning(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	adapter.observation = BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}
	adapter.stopLeavesRunning = true
	service := newTestBootstrapService(t, adapter, &fakeBootstrapDaemon{healthy: true})
	service.poll = pollBootstrapChecks(1)
	result, err := service.Stop(context.Background(), BootstrapMutationRequest{
		StateDir: testStateDir(t), Reason: "verify task release", IdempotencyKey: "stop-release",
		Deadline: time.Now().Add(time.Minute),
	})
	if !errors.Is(err, ErrBootstrapUnhealthy) {
		t.Fatalf("Stop() error = %v, want task-release failure", err)
	}
	rcpt, getErr := service.receiptStore.Get(result.ReceiptID)
	if getErr != nil {
		t.Fatalf("receipt Get() error = %v", getErr)
	}
	if rcpt.Outcome.Status != domain.OutcomeFailed ||
		!containsBootstrapEvidence(rcpt.EvidenceRefs, "bootstrap-task-stop-applied") ||
		!containsBootstrapEvidence(rcpt.EvidenceRefs, "bootstrap-task-still-running") {
		t.Fatalf("stop receipt outcome/evidence = %q/%v", rcpt.Outcome.Status, rcpt.EvidenceRefs)
	}
}

func TestBootstrapServiceStopSkipsFallbackEffectWhenTaskStopsDuringGrace(t *testing.T) {
	t.Parallel()

	for _, state := range []BootstrapState{BootstrapStopped, BootstrapAbsent} {
		t.Run(string(state), func(t *testing.T) {
			adapter := newFakeBootstrapAdapter()
			adapter.observation = BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}
			adapter.onInspect = func(call int) {
				if call == 3 {
					adapter.observation = BootstrapObservation{State: state, Exact: state != BootstrapAbsent}
				}
			}
			daemon := &fakeBootstrapDaemon{
				healthy:       true,
				releaseStates: []BootstrapDaemonReleaseState{BootstrapDaemonShutdownPending, BootstrapDaemonReleased},
			}
			service := newTestBootstrapService(t, adapter, daemon)
			service.poll = pollBootstrapChecks(1)

			result, err := service.Stop(context.Background(), BootstrapMutationRequest{
				StateDir: testStateDir(t), Reason: "observe task stop before fallback", IdempotencyKey: "stop-no-repeat-" + string(state),
				Deadline: time.Now().Add(time.Minute),
			})
			if err != nil || result.Status != BootstrapStopped || adapter.stopCalls != 0 || result.TaskStopApplied {
				t.Fatalf("Stop() = %#v, %v; task stops=%d", result, err, adapter.stopCalls)
			}
		})
	}
}

func TestBootstrapServiceStopRecordsTaskEffectWhenReleaseVerificationFails(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	adapter.observation = BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}
	daemon := &fakeBootstrapDaemon{
		releaseStates: []BootstrapDaemonReleaseState{BootstrapDaemonShutdownPending},
	}
	service := newTestBootstrapService(t, adapter, daemon)
	service.poll = pollBootstrapChecks(1)
	req := BootstrapMutationRequest{
		StateDir: testStateDir(t), Reason: "preserve fallback effect truth", IdempotencyKey: "stop-effect-truth",
		Deadline: time.Now().Add(time.Minute),
	}

	first, err := service.Stop(context.Background(), req)
	if !errors.Is(err, ErrBootstrapUnhealthy) || adapter.stopCalls != 1 || !first.TaskStopApplied {
		t.Fatalf("first Stop() = %#v, %v; task stops=%d", first, err, adapter.stopCalls)
	}
	rcpt, getErr := service.receiptStore.Get(first.ReceiptID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if rcpt.Outcome.Status != domain.OutcomeFailed || !containsBootstrapEvidence(rcpt.EvidenceRefs, "bootstrap-task-stop-applied") {
		t.Fatalf("receipt outcome/evidence = %q/%v", rcpt.Outcome.Status, rcpt.EvidenceRefs)
	}
	inspectCalls, releaseChecks := adapter.inspectCalls, daemon.releaseChecks
	second, replayErr := service.Stop(context.Background(), req)
	if !errors.Is(replayErr, ErrBootstrapPriorFailed) || !second.Replayed || second.Status != BootstrapFailed || !second.TaskStopApplied {
		t.Fatalf("replayed Stop() = %#v, %v", second, replayErr)
	}
	if adapter.stopCalls != 1 || adapter.inspectCalls != inspectCalls || daemon.releaseChecks != releaseChecks {
		t.Fatalf("failed replay repeated work: stops=%d inspect=%d->%d release=%d->%d",
			adapter.stopCalls, inspectCalls, adapter.inspectCalls, releaseChecks, daemon.releaseChecks)
	}
}

func TestBootstrapServiceStopKeepsForeignReleaseDriftFailClosed(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	adapter.observation = BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}
	daemon := &fakeBootstrapDaemon{
		releaseStates: []BootstrapDaemonReleaseState{BootstrapDaemonReleaseDrift},
	}
	service := newTestBootstrapService(t, adapter, daemon)
	_, err := service.Stop(context.Background(), BootstrapMutationRequest{
		StateDir: testStateDir(t), Reason: "reject foreign release", IdempotencyKey: "stop-foreign-release",
		Deadline: time.Now().Add(time.Minute),
	})
	if !errors.Is(err, ErrBootstrapDrift) || errors.Is(err, ErrBootstrapUnhealthy) || adapter.stopCalls != 0 {
		t.Fatalf("Stop() error=%v task stops=%d", err, adapter.stopCalls)
	}
}

func TestBootstrapServiceRemoveUsesRepairedStopFallback(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	adapter.observation = BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}
	daemon := &fakeBootstrapDaemon{
		healthy: true,
		releaseStates: []BootstrapDaemonReleaseState{
			BootstrapDaemonShutdownPending,
			BootstrapDaemonReleased,
		},
	}
	service := newTestBootstrapService(t, adapter, daemon)
	service.poll = pollBootstrapChecks(1)
	result, err := service.Remove(context.Background(), BootstrapMutationRequest{
		StateDir: testStateDir(t), Reason: "remove through exact stop fallback", IdempotencyKey: "remove-stop-fallback",
		Deadline: time.Now().Add(time.Minute),
	})
	if err != nil || result.Status != BootstrapAbsent || adapter.stopCalls != 1 || adapter.removeCalls != 1 || !result.TaskStopApplied {
		t.Fatalf("Remove() = %#v, %v; task stops=%d removals=%d", result, err, adapter.stopCalls, adapter.removeCalls)
	}
}

func TestBootstrapWaitReleasedRequiresRemainingOperationDeadline(t *testing.T) {
	t.Parallel()

	service := newTestBootstrapService(t, newFakeBootstrapAdapter(), &fakeBootstrapDaemon{})
	spec := BootstrapSpec{StateDir: testStateDir(t)}

	if err := service.waitBootstrapReleased(context.Background(), spec); !errors.Is(err, ErrBootstrapUnhealthy) {
		t.Fatalf("waitBootstrapReleased() without deadline error = %v, want unhealthy", err)
	}

	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if err := service.waitBootstrapReleased(expired, spec); !errors.Is(err, ErrBootstrapUnhealthy) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waitBootstrapReleased() expired deadline error = %v, want unhealthy deadline", err)
	}
}

func TestBootstrapWaitReleasedPreservesPollingFailure(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("synthetic release polling failure")
	service := newTestBootstrapService(t, newFakeBootstrapAdapter(), &fakeBootstrapDaemon{})
	service.poll = func(context.Context, time.Duration, func(context.Context) (bool, error)) (bool, error) {
		return false, wantErr
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := service.waitBootstrapReleased(ctx, BootstrapSpec{StateDir: testStateDir(t)}); !errors.Is(err, ErrBootstrapUnhealthy) || !errors.Is(err, wantErr) {
		t.Fatalf("waitBootstrapReleased() error = %v, want unhealthy polling failure", err)
	}
}

func containsBootstrapEvidence(evidence []string, want string) bool {
	return slices.Contains(evidence, want)
}
