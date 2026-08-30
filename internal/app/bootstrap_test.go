package app

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func TestBootstrapServiceEnsureCreatesStartsAndReplaysWithoutEffects(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	daemon := &fakeBootstrapDaemon{becomesHealthyAfter: 1}
	service := newTestBootstrapService(t, adapter, daemon)
	req := BootstrapMutationRequest{
		StateDir:       t.TempDir(),
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

	second, err := service.Ensure(context.Background(), req)
	if err != nil {
		t.Fatalf("replayed Ensure() error = %v", err)
	}
	if !second.Replayed || second.ReceiptID != first.ReceiptID {
		t.Fatalf("replayed Ensure() = %#v, first = %#v", second, first)
	}
	if adapter.installCalls != 1 || adapter.startCalls != 1 {
		t.Fatalf("replay repeated effects install=%d start=%d", adapter.installCalls, adapter.startCalls)
	}
}

func TestBootstrapServiceRefusesDriftWithoutMutation(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	adapter.observation = BootstrapObservation{State: BootstrapDrift, Reason: BootstrapReasonTaskMismatch}
	service := newTestBootstrapService(t, adapter, &fakeBootstrapDaemon{})

	_, err := service.Start(context.Background(), BootstrapMutationRequest{
		StateDir:       t.TempDir(),
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

func TestBootstrapServiceStopUsesGracefulThenExactTaskFallback(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	adapter.observation = BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}
	daemon := &fakeBootstrapDaemon{healthy: true, stopLeavesHealthy: true}
	service := newTestBootstrapService(t, adapter, daemon)

	result, err := service.Stop(context.Background(), BootstrapMutationRequest{
		StateDir:       t.TempDir(),
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

func TestBootstrapServiceRemoveRequiresExactOwnership(t *testing.T) {
	t.Parallel()

	adapter := newFakeBootstrapAdapter()
	adapter.observation = BootstrapObservation{State: BootstrapStopped, Exact: true}
	service := newTestBootstrapService(t, adapter, &fakeBootstrapDaemon{})

	result, err := service.Remove(context.Background(), BootstrapMutationRequest{
		StateDir:       t.TempDir(),
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
		"wrong SID":             func(spec *BootstrapSpec) { spec.UserSID = "S-1-5-21-2000" },
		"password logon":        func(spec *BootstrapSpec) { spec.LogonType = "Password" },
		"highest run level":     func(spec *BootstrapSpec) { spec.RunLevel = "Highest" },
		"missing logon trigger": func(spec *BootstrapSpec) { spec.LogonTrigger = false },
		"parallel instances":    func(spec *BootstrapSpec) { spec.MultipleInstances = "Parallel" },
		"bounded execution":     func(spec *BootstrapSpec) { spec.ExecutionTimeLimit = "PT30M" },
		"non-loopback listen":   func(spec *BootstrapSpec) { spec.ListenAddress = "0.0.0.0:8080" },
		"changed wrapper hash":  func(spec *BootstrapSpec) { spec.WrapperSHA256 = "not-a-hash" },
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
		StateDir: t.TempDir(), Reason: "install", IdempotencyKey: "foreign-daemon", Deadline: time.Now().Add(time.Minute),
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
		StateDir: t.TempDir(), Reason: "recover", IdempotencyKey: "failed-retry", Deadline: time.Now().Add(time.Minute),
	}
	if _, err := service.Start(context.Background(), req); !errors.Is(err, ErrBootstrapDrift) {
		t.Fatalf("first Start() error = %v, want drift", err)
	}
	adapter.observation = BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}
	daemon.healthy = true
	result, err := service.Start(context.Background(), req)
	if !errors.Is(err, ErrBootstrapPriorFailed) || !result.Replayed {
		t.Fatalf("replayed Start() = %#v, %v; want failed replay", result, err)
	}
	if adapter.startCalls != 0 {
		t.Fatalf("failed retry replayed start effect %d times", adapter.startCalls)
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
	observation  BootstrapObservation
	installCalls int
	startCalls   int
	stopCalls    int
	removeCalls  int
}

func newFakeBootstrapAdapter() *fakeBootstrapAdapter {
	return &fakeBootstrapAdapter{observation: BootstrapObservation{State: BootstrapAbsent}}
}

func (f *fakeBootstrapAdapter) Identity(context.Context) (BootstrapIdentity, error) {
	return BootstrapIdentity{Account: `SYNTHETIC\operator`, SID: "S-1-5-21-1000"}, nil
}

func (f *fakeBootstrapAdapter) Desired(context.Context, string, BootstrapIdentity) (BootstrapSpec, error) {
	return syntheticBootstrapSpec(), nil
}

func (f *fakeBootstrapAdapter) Inspect(context.Context, BootstrapSpec) (BootstrapObservation, error) {
	return f.observation, nil
}

func (f *fakeBootstrapAdapter) Install(context.Context, BootstrapSpec) error {
	f.installCalls++
	f.observation = BootstrapObservation{State: BootstrapStopped, Exact: true}
	return nil
}

func (f *fakeBootstrapAdapter) StartTask(context.Context, BootstrapSpec) error {
	f.startCalls++
	f.observation = BootstrapObservation{State: BootstrapHealthy, Exact: true, TaskRunning: true}
	return nil
}

func (f *fakeBootstrapAdapter) StopTask(context.Context, BootstrapSpec) error {
	f.stopCalls++
	f.observation = BootstrapObservation{State: BootstrapStopped, Exact: true}
	return nil
}

func (f *fakeBootstrapAdapter) Remove(context.Context, BootstrapSpec) error {
	f.removeCalls++
	f.observation = BootstrapObservation{State: BootstrapAbsent}
	return nil
}

type fakeBootstrapDaemon struct {
	healthy             bool
	stopLeavesHealthy   bool
	stopCalls           int
	healthChecks        int
	becomesHealthyAfter int
}

func (f *fakeBootstrapDaemon) Healthy(context.Context, string) (bool, error) {
	f.healthChecks++
	if f.becomesHealthyAfter > 0 && f.healthChecks > f.becomesHealthyAfter {
		f.healthy = true
	}
	if f.stopLeavesHealthy && f.stopCalls > 0 && f.healthChecks > 2 {
		f.healthy = false
	}
	return f.healthy, nil
}

func (f *fakeBootstrapDaemon) Stop(context.Context, string) error {
	f.stopCalls++
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
