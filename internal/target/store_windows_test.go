//go:build windows

package target

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/statedir"
)

func TestWindowsFreshStateStoreInitializesSavesAndLoads(t *testing.T) {
	state, err := statedir.Resolve(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err := state.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	store, err := NewStore(state.TargetsDir())
	if err != nil {
		t.Fatal(err)
	}
	want := testDefault(t, vmA)
	publication, err := store.Save(context.Background(), want)
	requireDurablePublication(t, "Save", publication, err)
	if err := state.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs after target protection: %v", err)
	}
	got, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.equal(want) {
		t.Fatalf("Load = %+v, want %+v", got, want)
	}
	requireStoredStateSecurity(t, store.path)
}
