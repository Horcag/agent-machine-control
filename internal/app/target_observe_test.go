package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/target"
)

type exactTargetObserver struct {
	MachineObserver
	calls   int
	inspect func(context.Context, string) (domain.MachineObservation, error)
}

func (o *exactTargetObserver) InspectMachine(ctx context.Context, id string) (domain.MachineObservation, error) {
	o.calls++
	return o.inspect(ctx, id)
}

func exactTargetService(t *testing.T, observation domain.MachineObservation, observer MachineObserver, refresh TargetRefresh) (*TargetService, *TrustedInventory) {
	t.Helper()
	inventory := targetInventory(t, nil, observation)
	store, _ := targetStore(t)
	value, err := target.NewDefault(observation.Locator, []string{"primary"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(t.Context(), value); err != nil {
		t.Fatal(err)
	}
	service, err := NewTargetService(inventory, store, WithTargetObserver(observer), WithTargetRefresh(refresh))
	if err != nil {
		t.Fatal(err)
	}
	return service, inventory
}

func TestTargetExactObservationIgnoresFleetAndRemainsFresh(t *testing.T) {
	initial := targetObservation(t, domain.LocalHostID, targetVMA, "enrolled")
	fresh := initial
	fresh.Name = "renamed"
	observer := &exactTargetObserver{inspect: func(_ context.Context, id string) (domain.MachineObservation, error) {
		if id != targetVMA {
			t.Fatalf("queried foreign target %q", id)
		}
		return fresh, nil
	}}
	refreshes := 0
	service, _ := exactTargetService(t, initial, observer, func(context.Context) error {
		refreshes++
		return errors.New("unrelated VM cannot be enumerated")
	})
	references := []string{"", "default", "primary", targetVMA, strings.ToUpper(targetVMA), initial.Locator.String()}
	for _, reference := range references {
		resolution, observation, err := service.ObserveTarget(t.Context(), reference)
		if err != nil || resolution.DisplayName != fresh.Name || observation.Name != fresh.Name || resolution.Locator != initial.Locator {
			t.Fatalf("ObserveTarget(%q) = %+v, %+v, %v", reference, resolution, observation, err)
		}
	}
	fresh.Name = "latest-name"
	resolution, err := service.ResolveTarget(t.Context(), "default")
	if err != nil || resolution.DisplayName != fresh.Name || observer.calls != len(references)+1 || refreshes != 0 {
		t.Fatalf("fresh resolution=%+v err=%v queries=%d fleet refreshes=%d", resolution, err, observer.calls, refreshes)
	}
}

func TestTargetAuthorityPlanningStillRequiresFleetRefresh(t *testing.T) {
	original := targetObservation(t, domain.LocalHostID, targetVMA, "enrolled")
	refreshes := 0
	service, _ := exactTargetService(t, original, &exactTargetObserver{}, func(context.Context) error {
		refreshes++
		return errors.New("unrelated VM prevents fleet discovery")
	})
	if _, err := service.ListLocalCandidates(t.Context()); !errors.Is(err, target.ErrInventoryRefresh) || refreshes != 1 {
		t.Fatalf("candidates skipped full refresh: %v, %d", err, refreshes)
	}
	if _, err := service.PrepareEnrollDefaultTarget(t.Context(), "", nil); !errors.Is(err, target.ErrInventoryRefresh) || refreshes != 2 {
		t.Fatalf("enroll skipped full refresh: %v, %d", err, refreshes)
	}
	if _, err := service.PrepareClearDefaultTarget(t.Context()); !errors.Is(err, target.ErrInventoryRefresh) || refreshes != 3 {
		t.Fatalf("clear skipped full refresh: %v, %d", err, refreshes)
	}
}

func TestTargetRejectsForeignReferencesBeforeAnyProviderQuery(t *testing.T) {
	observation := targetObservation(t, domain.LocalHostID, targetVMA, "enrolled")
	observer := &exactTargetObserver{inspect: func(context.Context, string) (domain.MachineObservation, error) {
		t.Fatal("invalid reference reached provider")
		return observation, nil
	}}
	refreshes := 0
	service, _ := exactTargetService(t, observation, observer, func(context.Context) error { refreshes++; return nil })
	for _, reference := range []string{observation.Name, "enrol", "PRIMARY", targetVMB, "remote:" + targetVMA, "local:" + targetVMB, " " + targetVMA, targetVMA + " ", "default ", "local:" + strings.ToUpper(targetVMA)} {
		if _, _, err := service.ObserveTarget(t.Context(), reference); !errors.Is(err, target.ErrDifferentTarget) {
			t.Fatalf("foreign reference %q error=%v", reference, err)
		}
	}
	if observer.calls != 0 || refreshes != 0 {
		t.Fatalf("queries=%d fleet=%d", observer.calls, refreshes)
	}
	// Legacy inventory-only services must validate before refreshing too.
	service.observer = nil
	if _, err := service.ResolveTarget(t.Context(), targetVMB); !errors.Is(err, target.ErrDifferentTarget) || refreshes != 0 {
		t.Fatalf("legacy invalid reference refreshed: err=%v refreshes=%d", err, refreshes)
	}
}

func TestTargetExactObservationFailsClosed(t *testing.T) {
	original := targetObservation(t, domain.LocalHostID, targetVMA, "enrolled")
	providerErr := errors.New("target unavailable")
	cases := []struct {
		name  string
		alter func(*domain.MachineObservation)
		err   error
	}{
		{name: "absent", alter: func(o *domain.MachineObservation) { *o = domain.MachineObservation{} }},
		{name: "foreign GUID", alter: func(o *domain.MachineObservation) { o.ID = targetVMB }},
		{name: "foreign valid identity", alter: func(o *domain.MachineObservation) { *o = targetObservation(t, domain.LocalHostID, targetVMB, "other") }},
		{name: "foreign host", alter: func(o *domain.MachineObservation) { *o = targetObservation(t, "remote", targetVMA, "other") }},
		{name: "missing host", alter: func(o *domain.MachineObservation) { o.HostID = "" }},
		{name: "missing locator", alter: func(o *domain.MachineObservation) { o.Locator = domain.MachineLocator{} }},
		{name: "malformed metrics", alter: func(o *domain.MachineObservation) { o.CPUUsagePercent = 101 }},
		{name: "malformed timestamp", alter: func(o *domain.MachineObservation) { o.ObservedAt = time.Time{} }},
		{name: "provider failure", err: providerErr},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			observer := &exactTargetObserver{inspect: func(context.Context, string) (domain.MachineObservation, error) {
				observed := original
				if tc.alter != nil {
					tc.alter(&observed)
				}
				return observed, tc.err
			}}
			service, _ := exactTargetService(t, original, observer, func(context.Context) error { t.Fatal("unexpected fleet query"); return nil })
			resolution, observed, err := service.ObserveTarget(t.Context(), "default")
			if err == nil || resolution != (TargetResolution{}) || observed.ID != "" {
				t.Fatalf("returned unsafe observation: %+v, %+v, %v", resolution, observed, err)
			}
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("lost provider error: %v", err)
			}
		})
	}
}

func TestTargetExactObservationHonorsDisabledHostAndCancellation(t *testing.T) {
	original := targetObservation(t, domain.LocalHostID, targetVMA, "enrolled")
	ctx, cancel := context.WithCancel(t.Context())
	observer := &exactTargetObserver{inspect: func(context.Context, string) (domain.MachineObservation, error) { cancel(); return original, nil }}
	service, inventory := exactTargetService(t, original, observer, func(context.Context) error { t.Fatal("unexpected fleet query"); return nil })
	if _, _, err := service.ObserveTarget(ctx, "default"); !errors.Is(err, context.Canceled) {
		t.Fatalf("post-query cancellation=%v", err)
	}
	if _, _, err := service.ObserveTarget(ctx, "default"); !errors.Is(err, context.Canceled) || observer.calls != 1 {
		t.Fatalf("pre-query cancellation=%v calls=%d", err, observer.calls)
	}
	if err := inventory.ReplaceHosts([]HostEntry{{ID: domain.LocalHostID, Address: "local", Enabled: false}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.ObserveTarget(t.Context(), "default"); !errors.Is(err, domain.ErrMachineHostDisabled) || observer.calls != 1 {
		t.Fatalf("disabled host=%v calls=%d", err, observer.calls)
	}
}

func TestTargetInventoryFallbackReusesRefreshedObservation(t *testing.T) {
	original := targetObservation(t, domain.LocalHostID, targetVMA, "enrolled")
	refreshes := 0
	service, inventory := exactTargetService(t, original, nil, func(context.Context) error { refreshes++; return nil })
	fresh := original
	fresh.Name = "refreshed-name"
	service.refresh = func(context.Context) error {
		refreshes++
		return inventory.ApplySnapshot(HostSnapshot{HostID: domain.LocalHostID, Health: HostHealthObserved, Machines: []domain.MachineObservation{fresh}})
	}
	_, observed, err := service.ObserveTarget(t.Context(), "primary")
	if err != nil || observed.Name != fresh.Name || refreshes != 1 {
		t.Fatalf("fallback observation=%+v err=%v refreshes=%d", observed, err, refreshes)
	}
}

func TestTargetExactObservationBoundsQueryDeadline(t *testing.T) {
	original := targetObservation(t, domain.LocalHostID, targetVMA, "enrolled")
	observer := &exactTargetObserver{inspect: func(ctx context.Context, _ string) (domain.MachineObservation, error) {
		deadline, ok := ctx.Deadline()
		remaining := time.Until(deadline)
		if !ok || remaining <= 0 || remaining > defaultLocalHostQueryTimeout {
			t.Fatalf("missing local query bound: deadline=%v remaining=%v", deadline, remaining)
		}
		upper := original
		upper.ID = strings.ToUpper(upper.ID)
		return upper, nil
	}}
	service, inventory := exactTargetService(t, original, observer, func(context.Context) error { return nil })
	resolution, observed, err := service.ObserveTarget(t.Context(), "default")
	if err != nil || resolution.ProviderVMID != targetVMA || observed.ID != targetVMA {
		t.Fatalf("normalized identity=%+v observation=%+v err=%v", resolution, observed, err)
	}
	if err := inventory.ReplaceHosts([]HostEntry{{ID: domain.LocalHostID, Address: "local", Enabled: true, QueryTimeout: time.Nanosecond}}); err != nil {
		t.Fatal(err)
	}
	observer.inspect = func(ctx context.Context, _ string) (domain.MachineObservation, error) {
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatalf("expired query reached observer without cancellation: %v", ctx.Err())
		}
		return original, errors.New("late provider failure")
	}
	if _, _, err := service.ObserveTarget(t.Context(), "default"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline failure=%v", err)
	}
}
