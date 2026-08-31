package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

// testStateDir returns a missing child so Windows state-root creation, owner, and DACL behavior
// are exercised instead of inheriting the ACL of t.TempDir().
func testStateDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "state")
}

func TestBootstrapServiceEnsureCreatesStartsAndReplaysWithoutEffects(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	daemon := &fakeBootstrapDaemon{becomesHealthyAfter: 1}
	service := newTestBootstrapService(t, adapter, daemon)
	req := BootstrapMutationRequest{
		StateDir:       testStateDir(t),
		Reason:         "install local control daemon",
		IdempotencyKey: "ensure-once",
		Deadline:       time.Now().Add(time.Minute),
	}

	first, err := service.Ensure(context.Background(), req)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if first.Status != BootstrapHealthy || first.Replayed {
		t.Fatalf("Ensure() = %#v, want healthy non-replay", first)
	}
	if adapter.installCalls != 1 || adapter.startCalls != 1 {
		t.Fatalf("effects install=%d start=%d, want 1 and 1", adapter.installCalls, adapter.startCalls)
	}
	adapter.inspectErr = errors.New("synthetic current state unavailable")
	daemon.healthErr = errors.New("synthetic endpoint unavailable")
	inspectCalls, healthChecks := adapter.inspectCalls, daemon.healthChecks

	second, err := service.Ensure(context.Background(), req)
	if err != nil {
		t.Fatalf("replayed Ensure() error = %v", err)
	}
	if !second.Replayed || second.ReceiptID != first.ReceiptID || second.Status != BootstrapHealthy {
		t.Fatalf("replayed Ensure() = %#v, first = %#v", second, first)
	}
	if adapter.installCalls != 1 || adapter.startCalls != 1 {
		t.Fatalf("replay repeated effects install=%d start=%d", adapter.installCalls, adapter.startCalls)
	}
	if adapter.inspectCalls != inspectCalls || daemon.healthChecks != healthChecks {
		t.Fatalf("replay consulted current state: inspect=%d->%d health=%d->%d", inspectCalls, adapter.inspectCalls, healthChecks, daemon.healthChecks)
	}
}

func TestBootstrapServiceRefusesDriftWithoutMutation(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	adapter.observation = BootstrapObservation{State: BootstrapDrift, Reason: BootstrapReasonTaskMismatch}
	service := newTestBootstrapService(t, adapter, &fakeBootstrapDaemon{})

	_, err := service.Start(context.Background(), BootstrapMutationRequest{
		StateDir:       testStateDir(t),
		Reason:         "recover daemon",
		IdempotencyKey: "start-drifted",
		Deadline:       time.Now().Add(time.Minute),
	})
	if !errors.Is(err, ErrBootstrapDrift) {
		t.Fatalf("Start() error = %v, want ErrBootstrapDrift", err)
	}
	if adapter.startCalls != 0 || adapter.stopCalls != 0 || adapter.removeCalls != 0 {
		t.Fatalf("drift triggered mutation: %#v", adapter)
	}
}

func TestBootstrapServiceStopTimeoutAndDriftRemainTruthful(t *testing.T) {
	t.Parallel()

	t.Run("operation timeout", func(t *testing.T) {
		adapter := newFakeBootstrapAdapter()
		adapter.observation = BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}
		service := newTestBootstrapService(t, adapter, &fakeBootstrapDaemon{healthy: true, stopLeavesHealthy: true})
		service.poll = func(context.Context, time.Duration, func(context.Context) (bool, error)) (bool, error) {
			return false, context.DeadlineExceeded
		}
		_, err := service.Stop(context.Background(), BootstrapMutationRequest{
			StateDir: testStateDir(t), Reason: "bounded stop", IdempotencyKey: "stop-timeout",
			Deadline: time.Now().Add(time.Minute),
		})
		if !errors.Is(err, ErrBootstrapUnhealthy) || !errors.Is(err, context.DeadlineExceeded) || adapter.stopCalls != 0 {
			t.Fatalf("Stop() error=%v fallback=%d", err, adapter.stopCalls)
		}
	})

	t.Run("task drift", func(t *testing.T) {
		adapter := newFakeBootstrapAdapter()
		adapter.observation = BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}
		service := newTestBootstrapService(t, adapter, &fakeBootstrapDaemon{healthy: true, stopLeavesHealthy: true})
		service.poll = func(ctx context.Context, _ time.Duration, check func(context.Context) (bool, error)) (bool, error) {
			adapter.observation = BootstrapObservation{State: BootstrapDrift, Reason: BootstrapReasonTaskMismatch}
			return check(ctx)
		}
		_, err := service.Stop(context.Background(), BootstrapMutationRequest{
			StateDir: testStateDir(t), Reason: "drifted stop", IdempotencyKey: "stop-drift",
			Deadline: time.Now().Add(time.Minute),
		})
		if !errors.Is(err, ErrBootstrapDrift) || adapter.stopCalls != 0 {
			t.Fatalf("Stop() error=%v fallback=%d", err, adapter.stopCalls)
		}
	})
}

func TestBootstrapServiceStopVerifiesFallbackTaskStopped(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	adapter.observation = BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}
	adapter.stopLeavesRunning = true
	service := newTestBootstrapService(t, adapter, &fakeBootstrapDaemon{})
	service.poll = pollBootstrapChecks(1)
	_, err := service.Stop(context.Background(), BootstrapMutationRequest{
		StateDir: testStateDir(t), Reason: "verify fallback", IdempotencyKey: "stop-verify",
		Deadline: time.Now().Add(time.Minute),
	})
	if !errors.Is(err, ErrBootstrapUnhealthy) || adapter.stopCalls != 1 {
		t.Fatalf("Stop() error=%v fallback=%d", err, adapter.stopCalls)
	}
}

func TestBootstrapServiceRemoveRequiresExactOwnership(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	adapter.observation = BootstrapObservation{State: BootstrapStopped, Exact: true}
	service := newTestBootstrapService(t, adapter, &fakeBootstrapDaemon{})

	result, err := service.Remove(context.Background(), BootstrapMutationRequest{
		StateDir:       testStateDir(t),
		Reason:         "uninstall operator-owned daemon",
		IdempotencyKey: "remove-owned",
		Deadline:       time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if result.Status != BootstrapAbsent || adapter.removeCalls != 1 {
		t.Fatalf("Remove() = %#v, calls=%d", result, adapter.removeCalls)
	}
}

func TestBootstrapSpecRejectsUnsafePrincipalAndTaskSettings(t *testing.T) {
	t.Parallel()

	identity := BootstrapIdentity{Account: `SYNTHETIC\operator`, SID: "S-1-5-21-1000"}
	tests := map[string]func(*BootstrapSpec){
		"wrong SID":              func(spec *BootstrapSpec) { spec.UserSID = "S-1-5-21-2000" },
		"password logon":         func(spec *BootstrapSpec) { spec.LogonType = "Password" },
		"highest run level":      func(spec *BootstrapSpec) { spec.RunLevel = "Highest" },
		"missing logon trigger":  func(spec *BootstrapSpec) { spec.LogonTrigger = false },
		"disabled delayed start": func(spec *BootstrapSpec) { spec.StartWhenAvailable = false },
		"parallel instances":     func(spec *BootstrapSpec) { spec.MultipleInstances = "Parallel" },
		"changed restart count":  func(spec *BootstrapSpec) { spec.RestartCount = 4 },
		"changed restart delay":  func(spec *BootstrapSpec) { spec.RestartInterval = "PT5M" },
		"bounded execution":      func(spec *BootstrapSpec) { spec.ExecutionTimeLimit = "PT30M" },
		"battery start blocked":  func(spec *BootstrapSpec) { spec.AllowStartOnBatteries = false },
		"battery stop enabled":   func(spec *BootstrapSpec) { spec.DontStopOnBatteries = false },
		"non-loopback listen":    func(spec *BootstrapSpec) { spec.ListenAddress = "0.0.0.0:8080" },
		"changed wrapper hash":   func(spec *BootstrapSpec) { spec.WrapperSHA256 = "not-a-hash" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			spec := syntheticBootstrapSpec()
			mutate(&spec)
			if err := spec.Validate(identity); err == nil {
				t.Fatalf("BootstrapSpec.Validate() accepted %s", name)
			}
		})
	}
}

func TestBootstrapIdentityRejectsServiceAccounts(t *testing.T) {
	t.Parallel()

	for _, sid := range []string{"S-1-5-18", "S-1-5-19", "S-1-5-20"} {
		identity := BootstrapIdentity{Account: `NT AUTHORITY\service`, SID: sid}
		if err := identity.Validate(); err == nil {
			t.Fatalf("BootstrapIdentity.Validate() accepted service SID %s", sid)
		}
	}
}

func TestBootstrapServiceStatusClassifiesOwnedStates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		observation BootstrapObservation
		healthy     bool
		want        BootstrapState
		wantErr     error
	}{
		{"absent", BootstrapObservation{State: BootstrapAbsent}, false, BootstrapAbsent, nil},
		{"stopped", BootstrapObservation{State: BootstrapStopped, Exact: true}, false, BootstrapStopped, nil},
		{"healthy", BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}, true, BootstrapHealthy, nil},
		{"drift", BootstrapObservation{State: BootstrapDrift, Reason: BootstrapReasonTaskMismatch}, false, BootstrapDrift, ErrBootstrapDrift},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			adapter := newFakeBootstrapAdapter()
			adapter.observation = tc.observation
			service := newTestBootstrapService(t, adapter, &fakeBootstrapDaemon{healthy: tc.healthy})
			result, err := service.Status(context.Background(), t.TempDir())
			if !errors.Is(err, tc.wantErr) || result.Status != tc.want {
				t.Fatalf("Status() = %#v, %v; want %q, %v", result, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestBootstrapEnsureRefusesDaemonWithoutOwnedTask(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	service := newTestBootstrapService(t, adapter, &fakeBootstrapDaemon{healthy: true})
	_, err := service.Ensure(context.Background(), BootstrapMutationRequest{
		StateDir: testStateDir(t), Reason: "install", IdempotencyKey: "foreign-daemon", Deadline: time.Now().Add(time.Minute),
	})
	if !errors.Is(err, ErrBootstrapDrift) || adapter.installCalls != 0 {
		t.Fatalf("Ensure() error=%v installCalls=%d", err, adapter.installCalls)
	}
}

func TestBootstrapFailedRetryDoesNotBecomeSuccessOrReplayEffects(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	adapter.observation = BootstrapObservation{State: BootstrapDrift, Reason: BootstrapReasonTaskMismatch}
	daemon := &fakeBootstrapDaemon{}
	service := newTestBootstrapService(t, adapter, daemon)
	req := BootstrapMutationRequest{
		StateDir: testStateDir(t), Reason: "recover", IdempotencyKey: "failed-retry", Deadline: time.Now().Add(time.Minute),
	}
	if _, err := service.Start(context.Background(), req); !errors.Is(err, ErrBootstrapDrift) {
		t.Fatalf("first Start() error = %v, want drift", err)
	}
	inspectCalls, healthChecks := adapter.inspectCalls, daemon.healthChecks
	adapter.inspectErr = errors.New("synthetic current state unavailable")
	daemon.healthErr = errors.New("synthetic endpoint unavailable")
	result, err := service.Start(context.Background(), req)
	if !errors.Is(err, ErrBootstrapPriorFailed) || !result.Replayed || result.Status != BootstrapFailed {
		t.Fatalf("replayed Start() = %#v, %v; want failed replay", result, err)
	}
	if adapter.startCalls != 0 {
		t.Fatalf("failed retry replayed start effect %d times", adapter.startCalls)
	}
	if adapter.inspectCalls != inspectCalls || daemon.healthChecks != healthChecks {
		t.Fatalf("failed replay consulted current state: inspect=%d->%d health=%d->%d", inspectCalls, adapter.inspectCalls, healthChecks, daemon.healthChecks)
	}
}

func TestBootstrapServiceStopBlocksFallbackOnPersistentDaemonDrift(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	adapter.observation = BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}
	daemon := &fakeBootstrapDaemon{healthy: true, stopLeavesHealthy: true}
	service := newTestBootstrapService(t, adapter, daemon)
	service.poll = func(ctx context.Context, _ time.Duration, check func(context.Context) (bool, error)) (bool, error) {
		daemon.releaseStates = []BootstrapDaemonReleaseState{BootstrapDaemonReleaseDrift}
		return check(ctx)
	}
	_, err := service.Stop(context.Background(), BootstrapMutationRequest{
		StateDir: testStateDir(t), Reason: "preserve ownership drift", IdempotencyKey: "stop-daemon-drift",
		Deadline: time.Now().Add(time.Minute),
	})
	if !errors.Is(err, ErrBootstrapDrift) || adapter.stopCalls != 0 {
		t.Fatalf("Stop() error=%v fallback=%d", err, adapter.stopCalls)
	}
}

func TestBootstrapServiceStopSurfacesOwnershipDriftAfterFallback(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	adapter.observation = BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}
	daemon := &fakeBootstrapDaemon{
		releaseStates: []BootstrapDaemonReleaseState{BootstrapDaemonReleased, BootstrapDaemonReleaseDrift},
	}
	service := newTestBootstrapService(t, adapter, daemon)
	_, err := service.Stop(context.Background(), BootstrapMutationRequest{
		StateDir: testStateDir(t), Reason: "verify ownership drift", IdempotencyKey: "stop-post-fallback-drift",
		Deadline: time.Now().Add(time.Minute),
	})
	if !errors.Is(err, ErrBootstrapDrift) || errors.Is(err, ErrBootstrapUnhealthy) || adapter.stopCalls != 1 {
		t.Fatalf("Stop() error=%v fallback=%d", err, adapter.stopCalls)
	}
}

func TestBootstrapReplayPreservesLifecycleTerminalSemantics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(*fakeBootstrapAdapter, *fakeBootstrapDaemon, *BootstrapService)
		run   func(*BootstrapService, context.Context, BootstrapMutationRequest) (BootstrapResult, error)
		want  BootstrapState
	}{
		{"ensure", func(_ *fakeBootstrapAdapter, daemon *fakeBootstrapDaemon, _ *BootstrapService) {
			daemon.becomesHealthyAfter = 1
		}, (*BootstrapService).Ensure, BootstrapHealthy},
		{"start", func(adapter *fakeBootstrapAdapter, daemon *fakeBootstrapDaemon, _ *BootstrapService) {
			adapter.observation = BootstrapObservation{State: BootstrapStopped, Exact: true}
			daemon.becomesHealthyAfter = 1
		}, (*BootstrapService).Start, BootstrapHealthy},
		{"stop", func(adapter *fakeBootstrapAdapter, daemon *fakeBootstrapDaemon, service *BootstrapService) {
			adapter.observation = BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}
			daemon.healthy, daemon.stopLeavesHealthy = true, true
			service.poll = pollBootstrapChecks(1)
		}, (*BootstrapService).Stop, BootstrapStopped},
		{"remove", func(adapter *fakeBootstrapAdapter, _ *fakeBootstrapDaemon, _ *BootstrapService) {
			adapter.observation = BootstrapObservation{State: BootstrapStopped, Exact: true}
		}, (*BootstrapService).Remove, BootstrapAbsent},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			adapter := newFakeBootstrapAdapter()
			daemon := &fakeBootstrapDaemon{}
			service := newTestBootstrapService(t, adapter, daemon)
			tc.setup(adapter, daemon, service)
			req := BootstrapMutationRequest{
				StateDir: testStateDir(t), Reason: "terminal replay", IdempotencyKey: "replay-" + tc.name,
				Deadline: time.Now().Add(time.Minute),
			}
			first, err := tc.run(service, context.Background(), req)
			if err != nil {
				t.Fatalf("first %s error = %v", tc.name, err)
			}
			effects := [4]int{adapter.installCalls, adapter.startCalls, adapter.stopCalls, adapter.removeCalls}
			adapter.inspectErr = errors.New("synthetic current state unavailable")
			daemon.healthErr = errors.New("synthetic endpoint unavailable")
			result, err := tc.run(service, context.Background(), req)
			if err != nil || !result.Replayed || result.ReceiptID != first.ReceiptID || result.Status != tc.want {
				t.Fatalf("replayed %s = %#v, %v; first=%#v", tc.name, result, err, first)
			}
			if got := [4]int{adapter.installCalls, adapter.startCalls, adapter.stopCalls, adapter.removeCalls}; got != effects {
				t.Fatalf("replayed %s effects=%v, want %v", tc.name, got, effects)
			}
		})
	}
}

func TestBootstrapReplayChangedRequestStillCollides(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	service := newTestBootstrapService(t, adapter, &fakeBootstrapDaemon{becomesHealthyAfter: 1})
	req := BootstrapMutationRequest{
		StateDir: testStateDir(t), Reason: "first intent", IdempotencyKey: "collision-key",
		Deadline: time.Now().Add(time.Minute),
	}
	if _, err := service.Ensure(context.Background(), req); err != nil {
		t.Fatalf("first Ensure() error = %v", err)
	}
	req.Reason = "changed intent"
	if _, err := service.Ensure(context.Background(), req); !errors.Is(err, receipt.ErrIdempotencyCollision) {
		t.Fatalf("changed Ensure() error = %v, want idempotency collision", err)
	}
}

func TestBootstrapMutationRejectsMissingAuditIdentity(t *testing.T) {
	t.Parallel()

	service := newTestBootstrapService(t, newFakeBootstrapAdapter(), &fakeBootstrapDaemon{})
	for _, req := range []BootstrapMutationRequest{
		{Reason: "reason", IdempotencyKey: "key"},
		{Reason: "", IdempotencyKey: "key", Deadline: time.Now().Add(time.Minute)},
		{Reason: "reason", IdempotencyKey: "", Deadline: time.Now().Add(time.Minute)},
	} {
		if _, err := service.Ensure(context.Background(), req); err == nil {
			t.Fatalf("Ensure() accepted invalid request %#v", req)
		}
	}
}

func TestBootstrapReceiptUsesExplicitSIDAndObservedStateWithoutRollbackClaim(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	service := newTestBootstrapService(t, adapter, &fakeBootstrapDaemon{becomesHealthyAfter: 1})
	result, err := service.Ensure(context.Background(), BootstrapMutationRequest{
		StateDir: testStateDir(t), Reason: "install owned daemon", IdempotencyKey: "receipt-truth",
		Deadline: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	rcpt, err := service.receiptStore.Get(result.ReceiptID)
	if err != nil {
		t.Fatalf("receipt Get() error = %v", err)
	}
	if rcpt.Actor != "windows-sid:S-1-5-21-1000" {
		t.Fatalf("receipt actor = %q, want explicit current SID", rcpt.Actor)
	}
	if rcpt.Class != domain.ClassDestructivePrivileged || rcpt.RollbackRef != "" {
		t.Fatalf("receipt class/rollback = %q/%q, want destructive without fabricated rollback", rcpt.Class, rcpt.RollbackRef)
	}
	if len(rcpt.EvidenceRefs) != 1 || rcpt.EvidenceRefs[0] != "bootstrap-state-healthy" {
		t.Fatalf("receipt evidence = %v, want observed healthy state", rcpt.EvidenceRefs)
	}
}

func TestBootstrapPartialInstallRecordsObservedStateAndExactRetryDoesNotRepeat(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	adapter.startErr = errors.New("synthetic start failure")
	service := newTestBootstrapService(t, adapter, &fakeBootstrapDaemon{})
	req := BootstrapMutationRequest{
		StateDir: testStateDir(t), Reason: "install then fail start", IdempotencyKey: "partial-install",
		Deadline: time.Now().Add(time.Minute),
	}
	first, err := service.Ensure(context.Background(), req)
	if err == nil || first.Status != BootstrapStopped {
		t.Fatalf("Ensure() = %#v, %v; want stopped partial failure", first, err)
	}
	rcpt, getErr := service.receiptStore.Get(first.ReceiptID)
	if getErr != nil {
		t.Fatalf("receipt Get() error = %v", getErr)
	}
	if rcpt.Outcome.Status != domain.OutcomeFailed || rcpt.RollbackRef != "" {
		t.Fatalf("partial receipt outcome/rollback = %q/%q", rcpt.Outcome.Status, rcpt.RollbackRef)
	}
	if len(rcpt.EvidenceRefs) != 1 || rcpt.EvidenceRefs[0] != "bootstrap-state-stopped" {
		t.Fatalf("partial receipt evidence = %v", rcpt.EvidenceRefs)
	}
	second, retryErr := service.Ensure(context.Background(), req)
	if !errors.Is(retryErr, ErrBootstrapPriorFailed) || !second.Replayed {
		t.Fatalf("exact retry = %#v, %v; want failed replay", second, retryErr)
	}
	if adapter.installCalls != 1 || adapter.startCalls != 1 {
		t.Fatalf("exact retry repeated effects install=%d start=%d", adapter.installCalls, adapter.startCalls)
	}
}

func TestBootstrapPartialStartRecordsRunningTaskState(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	adapter.observation = BootstrapObservation{State: BootstrapStopped, Exact: true}
	adapter.startErr = errors.New("synthetic post-start failure")
	adapter.startChangesState = true
	service := newTestBootstrapService(t, adapter, &fakeBootstrapDaemon{healthy: true})
	result, err := service.Start(context.Background(), BootstrapMutationRequest{
		StateDir: testStateDir(t), Reason: "start with post-effect failure", IdempotencyKey: "partial-start",
		Deadline: time.Now().Add(time.Minute),
	})
	if err == nil || result.Status != BootstrapHealthy {
		t.Fatalf("Start() = %#v, %v; want observed running partial effect", result, err)
	}
	rcpt, getErr := service.receiptStore.Get(result.ReceiptID)
	if getErr != nil || rcpt.Outcome.Status != domain.OutcomeFailed || rcpt.EvidenceRefs[0] != "bootstrap-state-healthy" {
		t.Fatalf("start receipt = %#v, %v", rcpt, getErr)
	}
}

func TestBootstrapPartialRemoveRecordsAbsentState(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	adapter.observation = BootstrapObservation{State: BootstrapStopped, Exact: true}
	adapter.removeErr = errors.New("synthetic post-remove failure")
	service := newTestBootstrapService(t, adapter, &fakeBootstrapDaemon{})
	result, err := service.Remove(context.Background(), BootstrapMutationRequest{
		StateDir: testStateDir(t), Reason: "remove with post-effect failure", IdempotencyKey: "partial-remove",
		Deadline: time.Now().Add(time.Minute),
	})
	if err == nil || result.Status != BootstrapAbsent {
		t.Fatalf("Remove() = %#v, %v; want observed absent partial effect", result, err)
	}
	rcpt, getErr := service.receiptStore.Get(result.ReceiptID)
	if getErr != nil || rcpt.Outcome.Status != domain.OutcomeFailed || rcpt.EvidenceRefs[0] != "bootstrap-state-absent" {
		t.Fatalf("remove receipt = %#v, %v", rcpt, getErr)
	}
}

func newTestBootstrapService(t *testing.T, adapter BootstrapAdapter, daemon BootstrapDaemon) *BootstrapService {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(root+"/audit", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root+"/receipts", 0700); err != nil {
		t.Fatal(err)
	}
	return NewBootstrapService(
		adapter,
		daemon,
		audit.NewStore(root+"/audit"),
		receipt.NewStore(root+"/receipts"),
	)
}

type fakeBootstrapAdapter struct {
	observation       BootstrapObservation
	identityErr       error
	desiredErr        error
	inspectErr        error
	inspectCalls      int
	installCalls      int
	startCalls        int
	stopCalls         int
	removeCalls       int
	startErr          error
	stopLeavesRunning bool
	startChangesState bool
	removeErr         error
	onInspect         func(int)
	onInspectContext  func(context.Context, int)
}

func newFakeBootstrapAdapter() *fakeBootstrapAdapter {
	return &fakeBootstrapAdapter{observation: BootstrapObservation{State: BootstrapAbsent}}
}

func (f *fakeBootstrapAdapter) Identity(context.Context) (BootstrapIdentity, error) {
	return BootstrapIdentity{Account: `SYNTHETIC\operator`, SID: "S-1-5-21-1000"}, f.identityErr
}

func (f *fakeBootstrapAdapter) Desired(context.Context, string, BootstrapIdentity) (BootstrapSpec, error) {
	return syntheticBootstrapSpec(), f.desiredErr
}

func (f *fakeBootstrapAdapter) Inspect(ctx context.Context, _ BootstrapSpec) (BootstrapObservation, error) {
	f.inspectCalls++
	if f.onInspect != nil {
		f.onInspect(f.inspectCalls)
	}
	if f.onInspectContext != nil {
		f.onInspectContext(ctx, f.inspectCalls)
	}
	return f.observation, f.inspectErr
}

func (f *fakeBootstrapAdapter) Install(context.Context, BootstrapSpec) error {
	f.installCalls++
	f.observation = BootstrapObservation{State: BootstrapStopped, Exact: true}
	return nil
}

func (f *fakeBootstrapAdapter) StartTask(context.Context, BootstrapSpec) error {
	f.startCalls++
	if f.startErr != nil {
		if f.startChangesState {
			f.observation = BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}
		}
		return f.startErr
	}
	f.observation = BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}
	return nil
}

func (f *fakeBootstrapAdapter) StopTask(context.Context, BootstrapSpec) error {
	f.stopCalls++
	if !f.stopLeavesRunning {
		f.observation = BootstrapObservation{State: BootstrapStopped, Exact: true}
	}
	return nil
}

func (f *fakeBootstrapAdapter) Remove(context.Context, BootstrapSpec) error {
	f.removeCalls++
	f.observation = BootstrapObservation{State: BootstrapAbsent}
	return f.removeErr
}

type fakeBootstrapDaemon struct {
	healthy             bool
	stopLeavesHealthy   bool
	stopCalls           int
	healthChecks        int
	becomesHealthyAfter int
	healthErr           error
	healthErrAfter      int
	onHealthCheck       func(int)
	stopErr             error
	releaseStates       []BootstrapDaemonReleaseState
	releaseErr          error
	releaseErrAfter     int
	releaseChecks       int
	onReleaseCheck      func(int)
}

func (f *fakeBootstrapDaemon) Healthy(context.Context, string) (bool, error) {
	f.healthChecks++
	if f.onHealthCheck != nil {
		f.onHealthCheck(f.healthChecks)
	}
	if f.healthErr != nil && f.healthChecks > f.healthErrAfter {
		return false, f.healthErr
	}
	if f.becomesHealthyAfter > 0 && f.healthChecks > f.becomesHealthyAfter {
		f.healthy = true
	}
	if f.stopLeavesHealthy && f.stopCalls > 0 && f.healthChecks > 2 {
		f.healthy = false
	}
	return f.healthy, nil
}

func (f *fakeBootstrapDaemon) ObserveRelease(ctx context.Context, stateDir string) (BootstrapDaemonReleaseObservation, error) {
	f.releaseChecks++
	if f.onReleaseCheck != nil {
		f.onReleaseCheck(f.releaseChecks)
	}
	if f.releaseErr != nil && f.releaseChecks > f.releaseErrAfter {
		return BootstrapDaemonReleaseObservation{}, f.releaseErr
	}
	if len(f.releaseStates) > 0 {
		index := f.releaseChecks - 1
		if index >= len(f.releaseStates) {
			index = len(f.releaseStates) - 1
		}
		return BootstrapDaemonReleaseObservation{State: f.releaseStates[index]}, nil
	}
	healthy, err := f.Healthy(ctx, stateDir)
	if err != nil {
		if errors.Is(err, ErrBootstrapDrift) {
			return BootstrapDaemonReleaseObservation{State: BootstrapDaemonReleaseDrift}, nil
		}
		if errors.Is(err, ErrBootstrapEndpointUnavailable) {
			return BootstrapDaemonReleaseObservation{State: BootstrapDaemonEndpointUnavailable}, nil
		}
		return BootstrapDaemonReleaseObservation{}, err
	}
	if healthy {
		return BootstrapDaemonReleaseObservation{State: BootstrapDaemonHealthy}, nil
	}
	return BootstrapDaemonReleaseObservation{State: BootstrapDaemonReleased}, nil
}

func pollBootstrapChecks(limit int) bootstrapPoller {
	return func(ctx context.Context, _ time.Duration, check func(context.Context) (bool, error)) (bool, error) {
		for range limit {
			done, err := check(ctx)
			if done || err != nil {
				return done, err
			}
		}
		return false, nil
	}
}

func (f *fakeBootstrapDaemon) Stop(context.Context, string) error {
	f.stopCalls++
	if f.stopErr != nil {
		f.healthy = false
		return f.stopErr
	}
	if !f.stopLeavesHealthy {
		f.healthy = false
	}
	return nil
}

func syntheticBootstrapSpec() BootstrapSpec {
	return BootstrapSpec{
		TaskPath: `\AgentMachineControl\`, TaskName: "amcd-current-user",
		ActionExecutable: `C:\Windows\System32\cmd.exe`, ActionArguments: `/d /c "C:\Users\operator\amcd.cmd"`,
		Account: `SYNTHETIC\operator`, UserSID: "S-1-5-21-1000", LogonType: "S4U", RunLevel: "Limited",
		LogonTrigger: true, StartWhenAvailable: true, MultipleInstances: "IgnoreNew",
		RestartCount: 3, RestartInterval: "PT1M", ExecutionTimeLimit: "PT0S",
		AllowStartOnBatteries: true, DontStopOnBatteries: true,
		Distro: "Synthetic-WSL", LinuxUser: "operator", StateDir: "/synthetic/state",
		WrapperPath: `C:\Users\operator\amcd.cmd`, WrapperSHA256: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		MetadataPath: `C:\Users\operator\amcd.json`, MetadataSHA256: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		BinaryPath:    "/usr/local/bin/amcd",
		BinarySHA256:  "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		WSLExecutable: `C:\Windows\System32\wsl.exe`, ListenAddress: "127.0.0.1:0",
	}
}
