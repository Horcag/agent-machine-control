package mcpadapter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/Horcag/agent-machine-control/internal/target"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type countingTargetBackend struct {
	*MockObserver
	listCalls       int
	inspectCalls    int
	checkpointCalls int
}

func (b *countingTargetBackend) ListMachines(ctx context.Context) ([]domain.MachineObservation, error) {
	b.listCalls++
	return b.MockObserver.ListMachines(ctx)
}

func (b *countingTargetBackend) InspectMachine(ctx context.Context, id string) (domain.MachineObservation, error) {
	b.inspectCalls++
	return b.MockObserver.InspectMachine(ctx, id)
}

func (b *countingTargetBackend) ListCheckpoints(ctx context.Context, id string) ([]domain.CheckpointObservation, error) {
	b.checkpointCalls++
	return b.MockObserver.ListCheckpoints(ctx, id)
}

func (b *countingTargetBackend) calls() int {
	return b.listCalls + b.inspectCalls + b.checkpointCalls
}

func requireProtectedTargetError(t *testing.T, result *mcp.CallToolResult) {
	t.Helper()
	if result == nil || !result.IsError || len(result.Content) != 1 {
		t.Fatalf("expected protected target tool error, got %+v", result)
	}
	message, ok := result.Content[0].(*mcp.TextContent)
	if !ok || message.Text != "protected target is unavailable" {
		t.Fatalf("protected target error = %+v", result.Content)
	}
}

func assertProductionAdapterFailsClosed(t *testing.T, adapter *Adapter) {
	t.Helper()
	checks := []struct {
		name string
		call func() (*mcp.CallToolResult, error)
	}{
		{name: "machine_list", call: func() (*mcp.CallToolResult, error) {
			result, _, err := adapter.MachineList(t.Context(), nil, MachineListInput{})
			return result, err
		}},
		{name: "machine_inspect", call: func() (*mcp.CallToolResult, error) {
			result, _, err := adapter.MachineInspect(t.Context(), nil, MachineInspectInput{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001"})
			return result, err
		}},
		{name: "checkpoint_list", call: func() (*mcp.CallToolResult, error) {
			result, _, err := adapter.CheckpointList(t.Context(), nil, CheckpointListInput{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001"})
			return result, err
		}},
		{name: "machine_start", call: func() (*mcp.CallToolResult, error) {
			result, _, err := adapter.MachineStart(t.Context(), nil, MachineStartInput{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "fail closed start", IdempotencyKey: "fail-closed-start", Timeout: "30s"})
			return result, err
		}},
		{name: "machine_stop", call: func() (*mcp.CallToolResult, error) {
			result, _, err := adapter.MachineStop(t.Context(), nil, MachineStopInput{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "fail closed stop", IdempotencyKey: "fail-closed-stop", Mode: "shutdown", Timeout: "30s"})
			return result, err
		}},
		{name: "checkpoint_create", call: func() (*mcp.CallToolResult, error) {
			result, _, err := adapter.CheckpointCreate(t.Context(), nil, CheckpointCreateInput{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "fail closed checkpoint", IdempotencyKey: "fail-closed-checkpoint", Name: "baseline", Timeout: "30s"})
			return result, err
		}},
		{name: "checkpoint_restore", call: func() (*mcp.CallToolResult, error) {
			result, _, err := adapter.CheckpointRestore(t.Context(), nil, CheckpointRestoreInput{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "fail closed restore", IdempotencyKey: "fail-closed-restore", CheckpointID: "c4a523d4-6b99-4d62-a5e2-4752c0f20002", Timeout: "30s"})
			return result, err
		}},
		{name: "session_open", call: func() (*mcp.CallToolResult, error) {
			result, _, err := adapter.SessionOpen(t.Context(), nil, SessionOpenInput{Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "fail closed session", IdempotencyKey: "fail-closed-session-open", Timeout: "30s"})
			return result, err
		}},
		{name: "session_write", call: func() (*mcp.CallToolResult, error) {
			result, _, err := adapter.SessionWrite(t.Context(), nil, SessionWriteInput{SessionID: "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4", Data: "exit\r\n", Reason: "fail closed write", IdempotencyKey: "fail-closed-session-write", Timeout: "30s"})
			return result, err
		}},
		{name: "session_control", call: func() (*mcp.CallToolResult, error) {
			result, _, err := adapter.SessionControl(t.Context(), nil, SessionControlInput{SessionID: "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4", Key: "ctrl-c", Reason: "fail closed control", IdempotencyKey: "fail-closed-session-control", Timeout: "30s"})
			return result, err
		}},
		{name: "session_close", call: func() (*mcp.CallToolResult, error) {
			result, _, err := adapter.SessionClose(t.Context(), nil, SessionCloseInput{SessionID: "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4", Reason: "fail closed close", IdempotencyKey: "fail-closed-session-close", Timeout: "30s"})
			return result, err
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			result, err := check.call()
			if err != nil {
				t.Fatal(err)
			}
			requireProtectedTargetError(t, result)
		})
	}
}

func TestProductionAdapterFailsClosedWithoutProtectedTargetState(t *testing.T) {
	missingStateDir := filepath.Join(t.TempDir(), "missing-state")
	for _, stateDir := range []string{"", missingStateDir} {
		t.Run("state-dir="+stateDir, func(t *testing.T) {
			backend := &countingTargetBackend{MockObserver: getTestObserver()}
			clientCalls := 0
			daemonServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				clientCalls++
			}))
			defer daemonServer.Close()

			adapter := NewAdapter(stateDir)
			adapter.discoveryService = app.NewDiscoveryService(backend)
			adapter.recoveryService = app.NewRecoveryService(backend, nil, nil, nil, nil)
			adapter.client = client.New(daemonServer.URL, "test-token")
			_ = adapter.BuildServer()

			assertProductionAdapterFailsClosed(t, adapter)

			if calls := backend.calls(); calls != 0 {
				t.Errorf("provider calls = %d, want 0", calls)
			}
			if clientCalls != 0 {
				t.Errorf("daemon client calls = %d, want 0", clientCalls)
			}
		})
	}
}

func TestCompatibilityAdapterAllowsOnlyExplicitTestFallback(t *testing.T) {
	adapter := &Adapter{allowUnscopedTestTargetFallback: true}
	machine, err := adapter.observeTargetMachine(t.Context(), "fallback", MachineDTO{ID: "fallback"})
	if err != nil || machine.ID != "fallback" {
		t.Fatalf("explicit test fallback = %+v, %v", machine, err)
	}

	production := NewAdapter("")
	if _, err := production.resolveTarget(t.Context(), "fallback"); err == nil || !strings.Contains(err.Error(), "protected target state") {
		t.Fatalf("production empty state resolution error = %v", err)
	}
}

//nolint:cyclop // The test verifies all observation boundaries against one enrolled target.
func TestMCPObservationUsesOnlyTheEnrolledTarget(t *testing.T) {
	const enrolledID = "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	const otherID = "c4a523d4-6b99-4d62-a5e2-4752c0f20002"
	state, err := statedir.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := state.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	store, err := target.NewStore(state.TargetsDir())
	if err != nil {
		t.Fatal(err)
	}
	locator, err := domain.NewMachineLocator(domain.LocalHostID, enrolledID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(context.Background(), mustTargetDefault(t, locator)); err != nil {
		t.Fatal(err)
	}
	inventory, err := app.NewTrustedInventory(nil)
	if err != nil {
		t.Fatal(err)
	}
	observed := getTestObserver().inspect
	observed.ID = enrolledID
	observed.HostID = domain.LocalHostID
	observed.Locator = locator
	service, err := app.NewTargetService(inventory, store, app.WithTargetRefresh(func(context.Context) error {
		return inventory.ApplySnapshot(app.HostSnapshot{HostID: domain.LocalHostID, Health: app.HostHealthObserved, Machines: []domain.MachineObservation{observed}})
	}))
	if err != nil {
		t.Fatal(err)
	}
	adapter := &Adapter{targetService: service, discoveryService: app.NewDiscoveryService(&MockObserver{inspect: observed})}
	toolError, result, err := adapter.MachineList(context.Background(), nil, MachineListInput{})
	if err != nil || toolError != nil || len(result.Machines) != 1 || result.Machines[0].ID != enrolledID {
		t.Fatalf("MachineList = error=%+v result=%+v err=%v", toolError, result, err)
	}
	toolError, _, err = adapter.MachineInspect(context.Background(), nil, MachineInspectInput{ID: otherID})
	if err != nil || toolError == nil || !toolError.IsError {
		t.Fatalf("MachineInspect(other target) = error=%+v err=%v", toolError, err)
	}
	observedMachine, err := adapter.observeTargetMachine(context.Background(), "local", MachineDTO{})
	if err != nil || observedMachine.ID != enrolledID {
		t.Fatalf("observeTargetMachine = %+v, %v", observedMachine, err)
	}
	checkpoint := domain.CheckpointObservation{ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20002", VMID: enrolledID, Name: "baseline", CreatedAt: observed.ObservedAt, ObservedAt: observed.ObservedAt, ObservationType: domain.ObservationObserved}
	adapter.recoveryService = app.NewRecoveryService(&MockObserver{checkpoints: []domain.CheckpointObservation{checkpoint}}, nil, nil, nil, nil)
	observedCheckpoint, err := adapter.observeTargetCheckpoint(context.Background(), "local", checkpoint.ID, "fallback")
	if err != nil || observedCheckpoint.ID != checkpoint.ID || observedCheckpoint.ObservationType != string(domain.ObservationObserved) {
		t.Fatalf("observeTargetCheckpoint = %+v, %v", observedCheckpoint, err)
	}
}

func TestMCPBuildsTargetServiceFromStateDirectory(t *testing.T) {
	state, err := statedir.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := state.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	service, err := NewAdapter(state.Root()).getTargetService()
	if err != nil || service == nil {
		t.Fatalf("getTargetService = %v, %v", service, err)
	}
	fallbackAdapter := &Adapter{allowUnscopedTestTargetFallback: true}
	machine, err := fallbackAdapter.observeTargetMachine(context.Background(), "fallback", MachineDTO{ID: "fallback"})
	if err != nil || machine.ID != "fallback" {
		t.Fatalf("fallback machine = %+v, %v", machine, err)
	}
	checkpoint, err := fallbackAdapter.observeTargetCheckpoint(context.Background(), "fallback", "checkpoint", "fallback")
	if err != nil || checkpoint.ObservationType != string(domain.ObservationInferred) {
		t.Fatalf("fallback checkpoint = %+v, %v", checkpoint, err)
	}
}

func mustTargetDefault(t *testing.T, locator domain.MachineLocator) target.Default {
	t.Helper()
	value, err := target.NewDefault(locator, []string{"local"})
	if err != nil {
		t.Fatal(err)
	}
	return value
}
