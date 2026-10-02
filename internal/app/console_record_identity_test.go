package app_test

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/target"
)

func TestConsoleRecordDefaultTargetSwitchRefusesFurtherCapture(t *testing.T) {
	provider := &boundsRecordingProviderFake{}
	f := newBoundsRecordingFixture(t, provider)
	store, err := target.NewStore(filepath.Join(f.root, "targets"))
	if err != nil {
		t.Fatal(err)
	}
	foreignLocator, err := domain.NewMachineLocator(domain.LocalHostID, "bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := target.NewDefault(foreignLocator, []string{"primary"})
	if err != nil {
		t.Fatal(err)
	}
	observations, err := f.backend.ListMachines(t.Context())
	if err != nil || len(observations) != 1 {
		t.Fatalf("initial inventory = %+v, %v", observations, err)
	}
	foreignObservation := observations[0].Clone()
	foreignObservation.Locator = foreignLocator
	foreignObservation.ID = foreignLocator.VMID
	foreignObservation.Name = "foreign-console-fixture"
	f.backend.listMachinesFn = func(context.Context) ([]domain.MachineObservation, error) {
		return append(observations, foreignObservation), nil
	}
	provider.onCapture = func(index int) error {
		if index != 0 {
			return nil
		}
		// Change protected enrollment while the first frame is being captured.
		_, err := store.Save(t.Context(), foreign)
		return err
	}
	out, err := f.service.Record(t.Context(), f.actor, app.ConsoleRecordRequest{
		Target: "default", Width: 8, Height: 4, Frames: 3, IntervalMillis: 100,
	})
	if !errors.Is(err, target.ErrDifferentTarget) {
		t.Fatalf("recording after enrollment switch = %v, want identity refusal", err)
	}
	if !reflect.DeepEqual(out, app.ConsoleRecording{}) {
		t.Fatalf("refused recording returned a partial artifact: %+v", out)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.captures != 1 || !reflect.DeepEqual(provider.targets, []string{desktopVMID}) {
		t.Fatalf("capture dispatch after switch = %d, %v", provider.captures, provider.targets)
	}
	current, err := store.Load(t.Context())
	if err != nil || current.Locator != foreignLocator {
		t.Fatalf("protected enrollment did not switch: %+v, %v", current, err)
	}
}
