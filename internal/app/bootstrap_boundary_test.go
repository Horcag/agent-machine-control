package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func TestBootstrapServiceStatusPropagatesIdentityAndDesiredFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(*fakeBootstrapAdapter, error)
	}{
		{name: "identity", setup: func(adapter *fakeBootstrapAdapter, err error) { adapter.identityErr = err }},
		{name: "desired spec", setup: func(adapter *fakeBootstrapAdapter, err error) { adapter.desiredErr = err }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wantErr := errors.New("synthetic " + tc.name + " failure")
			adapter := newFakeBootstrapAdapter()
			tc.setup(adapter, wantErr)
			service := newTestBootstrapService(t, adapter, &fakeBootstrapDaemon{})
			if _, err := service.Status(context.Background(), t.TempDir()); !errors.Is(err, wantErr) {
				t.Fatalf("Status() error = %v, want %v", err, wantErr)
			}
		})
	}
}

func TestBootstrapServiceStopTreatsAbsentAsAlreadyStopped(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	daemon := &fakeBootstrapDaemon{}
	service := newTestBootstrapService(t, adapter, daemon)
	req := BootstrapMutationRequest{
		StateDir: t.TempDir(), Reason: "stop absent bootstrap", IdempotencyKey: "stop-absent",
		Deadline: time.Now().Add(time.Minute),
	}
	first, err := service.Stop(context.Background(), req)
	if err != nil || first.Status != BootstrapStopped || adapter.stopCalls != 0 {
		t.Fatalf("first Stop() = %#v, %v; fallback calls=%d", first, err, adapter.stopCalls)
	}
	inspectCalls, healthChecks, releaseChecks := adapter.inspectCalls, daemon.healthChecks, daemon.releaseChecks
	adapter.inspectErr = errors.New("synthetic current task unavailable")
	daemon.healthErr = errors.New("synthetic current daemon unavailable")
	second, err := service.Stop(context.Background(), req)
	if err != nil || second.Status != BootstrapStopped || !second.Replayed || second.ReceiptID != first.ReceiptID {
		t.Fatalf("replayed Stop() = %#v, %v; first=%#v", second, err, first)
	}
	if adapter.inspectCalls != inspectCalls || daemon.healthChecks != healthChecks || daemon.releaseChecks != releaseChecks {
		t.Fatalf("replay inspected current state: inspect=%d->%d health=%d->%d release=%d->%d",
			inspectCalls, adapter.inspectCalls, healthChecks, daemon.healthChecks, releaseChecks, daemon.releaseChecks)
	}
}

func TestBootstrapServiceStartLeavesHealthyOwnedTaskAlone(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	adapter.observation = BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}
	service := newTestBootstrapService(t, adapter, &fakeBootstrapDaemon{healthy: true})
	result, err := service.Start(context.Background(), BootstrapMutationRequest{
		StateDir: t.TempDir(), Reason: "keep healthy bootstrap", IdempotencyKey: "start-healthy",
		Deadline: time.Now().Add(time.Minute),
	})
	if err != nil || result.Status != BootstrapHealthy || adapter.startCalls != 0 {
		t.Fatalf("Start() = %#v, %v; task starts=%d", result, err, adapter.startCalls)
	}
}

func TestBootstrapServiceStopPropagatesGracefulEndpointFailure(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("synthetic graceful stop failure")
	adapter := newFakeBootstrapAdapter()
	adapter.observation = BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}
	service := newTestBootstrapService(t, adapter, &fakeBootstrapDaemon{healthy: true, stopErr: wantErr})
	_, err := service.Stop(context.Background(), BootstrapMutationRequest{
		StateDir: t.TempDir(), Reason: "surface stop failure", IdempotencyKey: "stop-failure",
		Deadline: time.Now().Add(time.Minute),
	})
	if !errors.Is(err, wantErr) || adapter.stopCalls != 0 {
		t.Fatalf("Stop() error = %v; fallback calls=%d", err, adapter.stopCalls)
	}
}

func TestBootstrapServiceStopPropagatesPreflightObservationFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		daemon *fakeBootstrapDaemon
	}{
		{
			name:   "health observation",
			daemon: &fakeBootstrapDaemon{healthErr: errors.New("synthetic health observation failure")},
		},
		{
			name: "release observation",
			daemon: &fakeBootstrapDaemon{
				healthErr:  ErrBootstrapDrift,
				releaseErr: errors.New("synthetic release observation failure"),
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			adapter := newFakeBootstrapAdapter()
			adapter.observation = BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}
			service := newTestBootstrapService(t, adapter, tc.daemon)
			_, err := service.Stop(context.Background(), BootstrapMutationRequest{
				StateDir: t.TempDir(), Reason: "surface preflight observation failure",
				IdempotencyKey: "stop-preflight-" + tc.name, Deadline: time.Now().Add(time.Minute),
			})
			if err == nil || adapter.stopCalls != 0 || tc.daemon.stopCalls != 0 {
				t.Fatalf("Stop() error=%v graceful=%d task=%d", err, tc.daemon.stopCalls, adapter.stopCalls)
			}
		})
	}
}

func TestBootstrapServiceUsesInjectedClockForDeadlineAdmission(t *testing.T) {
	t.Parallel()

	now := time.Date(2040, 1, 2, 3, 4, 5, 0, time.UTC)
	root := t.TempDir()
	service := NewBootstrapService(
		newFakeBootstrapAdapter(),
		&fakeBootstrapDaemon{becomesHealthyAfter: 1},
		audit.NewStore(root+"/audit"),
		receipt.NewStore(root+"/receipts"),
		WithBootstrapClock(func() time.Time { return now }),
	)
	_, err := service.Ensure(context.Background(), BootstrapMutationRequest{
		StateDir: t.TempDir(), Reason: "clock-bound admission", IdempotencyKey: "injected-clock",
		Deadline: now.Add(-time.Minute),
	})
	if !errors.Is(err, domain.ErrMissingDeadline) {
		t.Fatalf("Ensure() error = %v, want deadline rejection from injected clock", err)
	}
}
