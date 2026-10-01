package mcpadapter

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/Horcag/agent-machine-control/internal/target"
)

func mcpExactTargetHarness(t *testing.T) (*Adapter, *countingTargetBackend) {
	t.Helper()
	backend := &countingTargetBackend{MockObserver: getTestObserver()}
	backend.listErr = errors.New("unrelated VM prevents fleet discovery")
	observed := backend.inspect
	locator, err := domain.NewMachineLocator(domain.LocalHostID, observed.ID)
	if err != nil {
		t.Fatal(err)
	}
	observed.HostID, observed.Locator = domain.LocalHostID, locator
	backend.inspect = observed
	state, err := statedir.Resolve(filepath.Join(t.TempDir(), "state"))
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
	enrolled, err := target.NewDefault(locator, []string{"primary"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(t.Context(), enrolled); err != nil {
		t.Fatal(err)
	}
	inventory, err := app.NewTrustedInventory(nil)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := app.NewTargetService(inventory, store, app.WithTargetObserver(backend), app.WithTargetRefresh(func(ctx context.Context) error { return app.RefreshLocalTrustedInventory(ctx, inventory, backend) }))
	if err != nil {
		t.Fatal(err)
	}
	adapter := &Adapter{targetService: targets, discoveryService: app.NewDiscoveryService(backend)}
	return adapter, backend
}

func TestMCPEnrolledListUsesSingleExactObservation(t *testing.T) {
	adapter, backend := mcpExactTargetHarness(t)
	toolErr, list, err := adapter.MachineList(t.Context(), nil, MachineListInput{})
	if toolErr != nil || err != nil || len(list.Machines) != 1 || list.Machines[0].ID != backend.inspect.ID {
		t.Fatalf("list=%+v error=%+v,%v", list, toolErr, err)
	}
	if backend.inspectCalls != 1 || backend.listCalls != 0 {
		t.Fatalf("queries=%d fleet=%d", backend.inspectCalls, backend.listCalls)
	}
}

func TestMCPEnrolledInspectRejectsForeignReferencesBeforeQuery(t *testing.T) {
	adapter, backend := mcpExactTargetHarness(t)
	toolErr, inspect, err := adapter.MachineInspect(t.Context(), nil, MachineInspectInput{ID: "primary"})
	if toolErr != nil || err != nil || inspect.Machine.ID != backend.inspect.ID {
		t.Fatalf("inspect=%+v error=%+v,%v", inspect, toolErr, err)
	}
	toolErr, _, err = adapter.MachineInspect(t.Context(), nil, MachineInspectInput{ID: backend.inspect.Name})
	if err != nil || toolErr == nil || !toolErr.IsError {
		t.Fatalf("invalid reference error=%+v,%v", toolErr, err)
	}
	if backend.inspectCalls != 1 || backend.listCalls != 0 {
		t.Fatalf("queries=%d fleet=%d", backend.inspectCalls, backend.listCalls)
	}
}

func TestMCPPostMutationObservationUsesSingleExactQuery(t *testing.T) {
	adapter, backend := mcpExactTargetHarness(t)
	dto, err := adapter.observeTargetMachine(t.Context(), "primary", MachineDTO{})
	if err != nil || dto.ID != backend.inspect.ID || backend.inspectCalls != 1 || backend.listCalls != 0 {
		t.Fatalf("post-mutation observation=%+v err=%v queries=%d fleet=%d", dto, err, backend.inspectCalls, backend.listCalls)
	}
}
