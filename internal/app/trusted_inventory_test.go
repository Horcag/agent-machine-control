package app_test

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func host(id, address string, enabled bool) app.HostEntry {
	return app.HostEntry{ID: domain.HostID(id), Address: address, Enabled: enabled, QueryTimeout: time.Second}
}

func localHost(enabled bool) app.HostEntry {
	return app.HostEntry{ID: domain.LocalHostID, Address: "local", Enabled: enabled, QueryTimeout: time.Second}
}

func observed(hostID domain.HostID, id, name string, at time.Time) domain.MachineObservation {
	locator, _ := domain.NewMachineLocator(hostID, id)
	return domain.MachineObservation{
		HostID:              hostID,
		Locator:             locator,
		ID:                  locator.VMID,
		Name:                name,
		State:               domain.MachineStateOff,
		RawState:            "Off",
		Generation:          2,
		Version:             "10.0",
		MemoryAssignedBytes: 1024,
		Capabilities:        domain.ReadOnlyMachineCapabilities(),
		ObservedAt:          at,
		ObservationType:     domain.ObservationObserved,
	}
}

func TestTrustedInventoryResolveExactReferences(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	inv, err := app.NewTrustedInventory([]app.HostEntry{
		host("host-a", "alpha.example", true),
		host("host-b", "bravo.example", true),
	})
	if err != nil {
		t.Fatalf("NewTrustedInventory failed: %v", err)
	}
	vmA := observed("host-a", "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", "vm-alpha", now)
	vmB := observed("host-b", "bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb", "vm-bravo", now)
	for _, snapshot := range []app.HostSnapshot{
		{HostID: "host-a", Health: app.HostHealthObserved, Machines: []domain.MachineObservation{vmA}},
		{HostID: "host-b", Health: app.HostHealthObserved, Machines: []domain.MachineObservation{vmB}},
	} {
		if err := inv.ApplySnapshot(snapshot); err != nil {
			t.Fatalf("ApplySnapshot failed: %v", err)
		}
	}
	if err := inv.SetMachineAliases(vmA.Locator, []string{"primary"}); err != nil {
		t.Fatalf("SetMachineAliases failed: %v", err)
	}

	for _, ref := range []string{vmA.Locator.String(), vmA.ID, vmA.Name, "primary"} {
		entry, err := inv.ResolveMachine(ref)
		if err != nil {
			t.Fatalf("ResolveMachine(%q) failed: %v", ref, err)
		}
		if entry.Locator != vmA.Locator {
			t.Fatalf("ResolveMachine(%q) = %s, want %s", ref, entry.Locator, vmA.Locator)
		}
	}
}

func TestTrustedInventoryInjectsLocalHostAndPreservesExplicitDisable(t *testing.T) {
	inv, err := app.NewTrustedInventory([]app.HostEntry{host("host-a", "alpha.example", true)})
	if err != nil {
		t.Fatalf("NewTrustedInventory failed: %v", err)
	}
	hosts := inv.Hosts()
	if len(hosts) != 2 || hosts[0].ID != domain.LocalHostID || !hosts[0].Enabled || hosts[0].Address != "local" {
		t.Fatalf("expected enabled local host injected first, got %+v", hosts)
	}

	disabled, err := app.NewTrustedInventory([]app.HostEntry{
		localHost(false),
		host("host-a", "alpha.example", true),
	})
	if err != nil {
		t.Fatalf("NewTrustedInventory with disabled local failed: %v", err)
	}
	hosts = disabled.Hosts()
	if len(hosts) != 2 || hosts[0].ID != domain.LocalHostID || hosts[0].Enabled {
		t.Fatalf("expected explicit local disable to be preserved, got %+v", hosts)
	}
}

func TestTrustedInventoryRejectsNonCanonicalHostAuthorities(t *testing.T) {
	tests := []struct {
		name    string
		hosts   []app.HostEntry
		wantErr error
	}{
		{
			name: "padded host cannot coexist with canonical host",
			hosts: []app.HostEntry{
				host("host-a", "alpha.example", true),
				host(" host-a ", "bravo.example", true),
			},
			wantErr: domain.ErrInvalidHostID,
		},
		{
			name:    "padded local cannot bypass automatic local host",
			hosts:   []app.HostEntry{host(" local ", "local", true)},
			wantErr: domain.ErrInvalidHostID,
		},
		{
			name:    "leading whitespace in address",
			hosts:   []app.HostEntry{host("host-a", " alpha.example", true)},
			wantErr: domain.ErrInvalidHostAddress,
		},
		{
			name:    "trailing whitespace in address",
			hosts:   []app.HostEntry{host("host-a", "alpha.example ", true)},
			wantErr: domain.ErrInvalidHostAddress,
		},
		{
			name: "canonical duplicate host ID",
			hosts: []app.HostEntry{
				host("host-a", "alpha.example", true),
				host("host-a", "bravo.example", true),
			},
			wantErr: domain.ErrInvalidHostID,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := app.NewTrustedInventory(tt.hosts); !errors.Is(err, tt.wantErr) {
				t.Fatalf("NewTrustedInventory expected %v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestTrustedInventoryReplacementAndLookupRejectNonCanonicalAuthorities(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	inv, err := app.NewTrustedInventory([]app.HostEntry{host("host-a", "alpha.example", true)})
	if err != nil {
		t.Fatalf("NewTrustedInventory failed: %v", err)
	}
	if err := inv.ReplaceHosts([]app.HostEntry{host(" host-b ", "bravo.example", true)}); !errors.Is(err, domain.ErrInvalidHostID) {
		t.Fatalf("ReplaceHosts expected ErrInvalidHostID, got %v", err)
	}
	hosts := inv.Hosts()
	if len(hosts) != 2 || hosts[1].ID != "host-a" {
		t.Fatalf("rejected replacement changed trusted hosts: %+v", hosts)
	}

	vm := observed("host-a", "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", "vm-alpha", now)
	if err := inv.ApplySnapshot(app.HostSnapshot{HostID: "host-a", Health: app.HostHealthObserved, Machines: []domain.MachineObservation{vm}}); err != nil {
		t.Fatalf("ApplySnapshot failed: %v", err)
	}
	if _, err := inv.ResolveMachine(" " + vm.Locator.String() + " "); !errors.Is(err, domain.ErrMachineReferenceMiss) {
		t.Fatalf("ResolveMachine with padded canonical locator expected ErrMachineReferenceMiss, got %v", err)
	}
	entry, err := inv.ResolveMachine(vm.Locator.String())
	if err != nil || entry.Locator != vm.Locator {
		t.Fatalf("canonical locator lookup = %+v, %v; want %s", entry, err, vm.Locator)
	}
}

func TestHostEntryRejectsInvalidTimeoutsAndLocalAddress(t *testing.T) {
	if err := (app.HostEntry{ID: "host-a", Address: "alpha.example", Enabled: true, QueryTimeout: -time.Second}).Validate(); err == nil {
		t.Fatal("expected negative query timeout to be rejected")
	}
	if err := (app.HostEntry{ID: domain.LocalHostID, Address: "alpha.example", Enabled: true}).Validate(); !errors.Is(err, domain.ErrInvalidHostAddress) {
		t.Fatalf("expected invalid local address error, got %v", err)
	}
}

func TestTrustedInventoryAmbiguousDuplicateNameGUIDAndAlias(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	inv, err := app.NewTrustedInventory([]app.HostEntry{
		host("host-a", "alpha.example", true),
		host("host-b", "bravo.example", true),
	})
	if err != nil {
		t.Fatalf("NewTrustedInventory failed: %v", err)
	}
	sharedID := "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
	vmA := observed("host-a", sharedID, "shared-name", now)
	vmB := observed("host-b", sharedID, "shared-name", now)
	if err := inv.ApplySnapshot(app.HostSnapshot{HostID: "host-a", Health: app.HostHealthObserved, Machines: []domain.MachineObservation{vmA}}); err != nil {
		t.Fatalf("ApplySnapshot host-a failed: %v", err)
	}
	if err := inv.ApplySnapshot(app.HostSnapshot{HostID: "host-b", Health: app.HostHealthObserved, Machines: []domain.MachineObservation{vmB}}); err != nil {
		t.Fatalf("ApplySnapshot host-b failed: %v", err)
	}
	for _, ref := range []string{sharedID, "shared-name"} {
		if _, err := inv.ResolveMachine(ref); !errors.Is(err, domain.ErrMachineReferenceAmbig) {
			t.Fatalf("ResolveMachine(%q) expected ambiguity, got %v", ref, err)
		}
	}
	if err := inv.SetMachineAliases(vmA.Locator, []string{"shared-name"}); !errors.Is(err, domain.ErrMachineReferenceAmbig) {
		t.Fatalf("expected alias/display collision to fail closed, got %v", err)
	}
}

func TestTrustedInventoryRejectsAliasCanonicalCollisionsAndSameHostDuplicateIDs(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	inv, err := app.NewTrustedInventory([]app.HostEntry{host("host-a", "alpha.example", true)})
	if err != nil {
		t.Fatalf("NewTrustedInventory failed: %v", err)
	}
	id := "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
	vm := observed("host-a", id, "vm-alpha", now)
	if err := inv.ApplySnapshot(app.HostSnapshot{HostID: "host-a", Health: app.HostHealthObserved, Machines: []domain.MachineObservation{vm}}); err != nil {
		t.Fatalf("ApplySnapshot failed: %v", err)
	}
	if err := inv.SetMachineAliases(vm.Locator, []string{id}); !errors.Is(err, domain.ErrMachineReferenceAmbig) {
		t.Fatalf("expected GUID alias collision to fail closed, got %v", err)
	}
	if err := inv.SetMachineAliases(vm.Locator, []string{vm.Locator.String()}); !errors.Is(err, domain.ErrMachineReferenceAmbig) {
		t.Fatalf("expected locator alias collision to fail closed, got %v", err)
	}
	dupErr := inv.ApplySnapshot(app.HostSnapshot{
		HostID: "host-a",
		Health: app.HostHealthObserved,
		Machines: []domain.MachineObservation{
			observed("host-a", id, "vm-alpha", now),
			observed("host-a", id, "vm-alpha-dup", now),
		},
	})
	if !errors.Is(dupErr, domain.ErrMachineReferenceAmbig) {
		t.Fatalf("expected same-host duplicate VM ID ambiguity, got %v", dupErr)
	}
}

func TestTrustedInventoryRejectsObservedSnapshotWithErrorAndUnstampedObservedEntry(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	inv, err := app.NewTrustedInventory([]app.HostEntry{host("host-a", "alpha.example", true)})
	if err != nil {
		t.Fatalf("NewTrustedInventory failed: %v", err)
	}
	vm := observed("host-a", "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", "vm-alpha", now)
	err = inv.ApplySnapshot(app.HostSnapshot{
		HostID:   "host-a",
		Health:   app.HostHealthObserved,
		Machines: []domain.MachineObservation{vm},
		Err:      errors.New("synthetic partial failure"),
	})
	if err == nil {
		t.Fatal("expected observed snapshot with error to be rejected")
	}
	entry := app.MachineIndexEntry{
		Locator:        vm.Locator,
		DisplayName:    vm.Name,
		LastObservedAt: now,
		LastStatus:     app.MachineIndexObserved,
	}
	if err := entry.Validate(); !errors.Is(err, domain.ErrInvalidMachineLocator) {
		t.Fatalf("expected unstamped observed index entry to be rejected, got %v", err)
	}
}

func TestTrustedInventoryLocatorStableAcrossAddressAndRename(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	inv, err := app.NewTrustedInventory([]app.HostEntry{host("host-a", "old.example", true)})
	if err != nil {
		t.Fatalf("NewTrustedInventory failed: %v", err)
	}
	id := "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
	if err := inv.ApplySnapshot(app.HostSnapshot{HostID: "host-a", Health: app.HostHealthObserved, Machines: []domain.MachineObservation{observed("host-a", id, "old-name", now)}}); err != nil {
		t.Fatalf("ApplySnapshot initial failed: %v", err)
	}
	initial, err := inv.ResolveMachine("old-name")
	if err != nil {
		t.Fatalf("ResolveMachine initial failed: %v", err)
	}
	if err := inv.ReplaceHosts([]app.HostEntry{host("host-a", "new.example", true)}); err != nil {
		t.Fatalf("ReplaceHosts failed: %v", err)
	}
	if err := inv.ApplySnapshot(app.HostSnapshot{HostID: "host-a", Health: app.HostHealthObserved, Machines: []domain.MachineObservation{observed("host-a", id, "new-name", now.Add(time.Minute))}}); err != nil {
		t.Fatalf("ApplySnapshot rename failed: %v", err)
	}
	renamed, err := inv.ResolveMachine("new-name")
	if err != nil {
		t.Fatalf("ResolveMachine renamed failed: %v", err)
	}
	if renamed.Locator != initial.Locator {
		t.Fatalf("locator changed across host address/name change: got %s want %s", renamed.Locator, initial.Locator)
	}
	if _, err := inv.ResolveMachine("old-name"); !errors.Is(err, domain.ErrMachineReferenceMiss) {
		t.Fatalf("old display name should no longer resolve, got %v", err)
	}
}

func TestTrustedInventoryStampsNestedObservationLocator(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	inv, err := app.NewTrustedInventory([]app.HostEntry{host("host-a", "alpha.example", true)})
	if err != nil {
		t.Fatalf("NewTrustedInventory failed: %v", err)
	}
	raw := observed("host-a", "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", "vm-alpha", now)
	raw.HostID = ""
	raw.Locator = domain.MachineLocator{}
	if err := inv.ApplySnapshot(app.HostSnapshot{HostID: "host-a", Health: app.HostHealthObserved, Machines: []domain.MachineObservation{raw}}); err != nil {
		t.Fatalf("ApplySnapshot failed: %v", err)
	}
	entry, err := inv.ResolveMachine(raw.ID)
	if err != nil {
		t.Fatalf("ResolveMachine failed: %v", err)
	}
	if entry.Observation.HostID != entry.Locator.HostID || entry.Observation.Locator != entry.Locator {
		t.Fatalf("nested observation not stamped with index locator: %+v", entry)
	}
	if err := entry.Validate(); err != nil {
		t.Fatalf("entry with nested observation should validate: %v", err)
	}
}

func TestTrustedInventoryDisabledStaleUnavailableAndDeniedRemainDistinct(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	id := "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
	tests := []struct {
		name      string
		host      app.HostEntry
		health    app.HostHealth
		wantErrIs error
	}{
		{"disabled", host("host-a", "alpha.example", false), app.HostHealthObserved, domain.ErrMachineHostDisabled},
		{"stale", host("host-a", "alpha.example", true), app.HostHealthStale, domain.ErrMachineReferenceStale},
		{"unavailable", host("host-a", "alpha.example", true), app.HostHealthUnavailable, domain.ErrMachineHostUnavailable},
		{"access denied", host("host-a", "alpha.example", true), app.HostHealthAccessDenied, domain.ErrMachineAccessDenied},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv, err := app.NewTrustedInventory([]app.HostEntry{host("host-a", "alpha.example", true)})
			if err != nil {
				t.Fatalf("NewTrustedInventory failed: %v", err)
			}
			vm := observed("host-a", id, "vm-alpha", now)
			if err := inv.ApplySnapshot(app.HostSnapshot{HostID: "host-a", Health: app.HostHealthObserved, Machines: []domain.MachineObservation{vm}}); err != nil {
				t.Fatalf("ApplySnapshot observed failed: %v", err)
			}
			if err := inv.ReplaceHosts([]app.HostEntry{tt.host}); err != nil {
				t.Fatalf("ReplaceHosts failed: %v", err)
			}
			if tt.health != app.HostHealthObserved {
				if err := inv.ApplySnapshot(app.HostSnapshot{HostID: "host-a", Health: tt.health}); err != nil {
					t.Fatalf("ApplySnapshot health failed: %v", err)
				}
			}
			if _, err := inv.ResolveMachine(id); !errors.Is(err, tt.wantErrIs) {
				t.Fatalf("ResolveMachine expected %v, got %v", tt.wantErrIs, err)
			}
		})
	}
}

func TestTrustedInventoryMultipleInactiveMatchesAreAmbiguous(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	inv, err := app.NewTrustedInventory([]app.HostEntry{
		host("host-a", "alpha.example", true),
		host("host-b", "bravo.example", true),
	})
	if err != nil {
		t.Fatalf("NewTrustedInventory failed: %v", err)
	}
	for _, hostID := range []domain.HostID{"host-a", "host-b"} {
		if err := inv.ApplySnapshot(app.HostSnapshot{
			HostID:   hostID,
			Health:   app.HostHealthObserved,
			Machines: []domain.MachineObservation{observed(hostID, "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", "shared-name", now)},
		}); err != nil {
			t.Fatalf("ApplySnapshot %s failed: %v", hostID, err)
		}
		if err := inv.ApplySnapshot(app.HostSnapshot{HostID: hostID, Health: app.HostHealthUnavailable}); err != nil {
			t.Fatalf("ApplySnapshot unavailable %s failed: %v", hostID, err)
		}
	}
	if _, err := inv.ResolveMachine("shared-name"); !errors.Is(err, domain.ErrMachineReferenceAmbig) {
		t.Fatalf("expected multiple unavailable matches to be ambiguous, got %v", err)
	}
}

func TestRefreshTrustedInventoryDeterministicOrderConcurrencyCancellationAndPartialFailure(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	inv, err := app.NewTrustedInventory([]app.HostEntry{
		localHost(false),
		host("host-a", "alpha.example", true),
		host("host-b", "bravo.example", true),
		host("host-c", "charlie.example", true),
	})
	if err != nil {
		t.Fatalf("NewTrustedInventory failed: %v", err)
	}
	var active atomic.Int32
	var maxActive atomic.Int32
	block := make(chan struct{})
	started := make(chan struct{}, 3)
	factory := func(h app.HostEntry) app.TrustedHostObserver {
		return observerFunc(func(ctx context.Context) ([]domain.MachineObservation, error) {
			trackAppActive(&active, &maxActive)
			started <- struct{}{}
			select {
			case <-block:
			case <-ctx.Done():
				active.Add(-1)
				return nil, ctx.Err()
			}
			defer active.Add(-1)
			if h.ID == "host-b" {
				return nil, domain.ErrMachineAccessDenied
			}
			return []domain.MachineObservation{observed(h.ID, "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", h.ID.String()+"-vm", now)}, nil
		})
	}
	done := make(chan []app.HostSnapshot, 1)
	go func() {
		snapshots, _ := app.RefreshTrustedInventory(context.Background(), inv, factory, 2)
		done <- snapshots
	}()
	<-started
	<-started
	if got := maxActive.Load(); got != 2 {
		t.Fatalf("expected concurrency cap 2, got %d", got)
	}
	close(block)
	snapshots := <-done
	gotIDs := []domain.HostID{snapshots[0].HostID, snapshots[1].HostID, snapshots[2].HostID, snapshots[3].HostID}
	wantIDs := []domain.HostID{domain.LocalHostID, "host-a", "host-b", "host-c"}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("snapshot order = %v, want %v", gotIDs, wantIDs)
	}
	if snapshots[2].Health != app.HostHealthAccessDenied {
		t.Fatalf("expected host-b access denied, got %+v", snapshots[2])
	}
}

func TestRefreshTrustedInventoryCancellationMarksPriorObservationUnavailable(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	cancelInv, err := app.NewTrustedInventory([]app.HostEntry{host("host-a", "alpha.example", true)})
	if err != nil {
		t.Fatalf("NewTrustedInventory failed: %v", err)
	}
	id := "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
	if err := cancelInv.ApplySnapshot(app.HostSnapshot{
		HostID:   "host-a",
		Health:   app.HostHealthObserved,
		Machines: []domain.MachineObservation{observed("host-a", id, "vm-alpha", now)},
	}); err != nil {
		t.Fatalf("ApplySnapshot before cancellation failed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int32
	snapshots, err := app.RefreshTrustedInventory(ctx, cancelInv, func(h app.HostEntry) app.TrustedHostObserver {
		return observerFunc(func(context.Context) ([]domain.MachineObservation, error) {
			calls.Add(1)
			return []domain.MachineObservation{observed(h.ID, id, "vm-alpha", now)}, nil
		})
	}, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if len(snapshots) != 2 || snapshots[1].HostID != "host-a" || snapshots[1].Health != app.HostHealthUnavailable {
		t.Fatalf("expected cancellation to mark enabled host unavailable, got %+v", snapshots)
	}
	if calls.Load() != 0 {
		t.Fatalf("pre-canceled refresh dispatched %d observers", calls.Load())
	}
	if _, err := cancelInv.ResolveMachine(id); !errors.Is(err, domain.ErrMachineHostUnavailable) {
		t.Fatalf("expected canceled refresh to mark prior observation unavailable, got %v", err)
	}
}

func trackAppActive(active, maxActive *atomic.Int32) {
	current := active.Add(1)
	for {
		seen := maxActive.Load()
		if current <= seen || maxActive.CompareAndSwap(seen, current) {
			return
		}
	}
}

func TestRefreshTrustedInventoryNilObserverFailsClosed(t *testing.T) {
	inv, err := app.NewTrustedInventory([]app.HostEntry{localHost(false), host("host-a", "alpha.example", true)})
	if err != nil {
		t.Fatalf("NewTrustedInventory failed: %v", err)
	}
	snapshots, err := app.RefreshTrustedInventory(context.Background(), inv, func(app.HostEntry) app.TrustedHostObserver {
		return nil
	}, 1)
	if err != nil {
		t.Fatalf("RefreshTrustedInventory should return snapshots for nil observer, got %v", err)
	}
	if len(snapshots) != 2 || snapshots[1].HostID != "host-a" || snapshots[1].Health != app.HostHealthUnavailable {
		t.Fatalf("expected nil observer to mark host unavailable, got %+v", snapshots)
	}
}

type observerFunc func(context.Context) ([]domain.MachineObservation, error)

func (f observerFunc) ListMachines(ctx context.Context) ([]domain.MachineObservation, error) {
	return f(ctx)
}

func TestTrustedInventoryRepeatedRefreshMarksRemovedMachineStale(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	inv, err := app.NewTrustedInventory([]app.HostEntry{host("host-a", "alpha.example", true)})
	if err != nil {
		t.Fatalf("NewTrustedInventory failed: %v", err)
	}
	id := "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
	if err := inv.ApplySnapshot(app.HostSnapshot{HostID: "host-a", Health: app.HostHealthObserved, Machines: []domain.MachineObservation{observed("host-a", id, "vm-alpha", now)}}); err != nil {
		t.Fatalf("ApplySnapshot initial failed: %v", err)
	}
	if err := inv.ApplySnapshot(app.HostSnapshot{HostID: "host-a", Health: app.HostHealthObserved, Machines: nil}); err != nil {
		t.Fatalf("ApplySnapshot empty refresh failed: %v", err)
	}
	if _, err := inv.ResolveMachine(id); !errors.Is(err, domain.ErrMachineReferenceStale) {
		t.Fatalf("expected stale after repeated empty refresh, got %v", err)
	}
}
