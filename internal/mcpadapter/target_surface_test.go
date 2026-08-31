package mcpadapter

import (
	"context"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/Horcag/agent-machine-control/internal/target"
)

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
	fallbackAdapter := &Adapter{}
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
