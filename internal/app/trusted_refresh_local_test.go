package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/target"
)

func TestLocalRefreshReportsUnavailableInsteadOfEmptyCandidates(t *testing.T) {
	inv, err := app.NewTrustedInventory(nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := target.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service, err := app.NewTargetService(inv, store, app.WithTargetRefresh(func(ctx context.Context) error {
		return app.RefreshLocalTrustedInventory(ctx, inv, observerFunc(func(context.Context) ([]domain.MachineObservation, error) {
			return nil, context.DeadlineExceeded
		}))
	}))
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := service.ListLocalCandidates(context.Background())
	if candidates != nil || !errors.Is(err, target.ErrInventoryRefresh) {
		t.Fatalf("ListLocalCandidates = %v, %v; want inventory refresh failure", candidates, err)
	}
	if _, err := service.PrepareEnrollDefaultTarget(context.Background(), "", nil); !errors.Is(err, target.ErrInventoryRefresh) {
		t.Fatalf("PrepareEnrollDefaultTarget = %v; want inventory refresh failure", err)
	}
}

func TestLocalRefreshClassifiesObserverFailure(t *testing.T) {
	for _, tc := range []struct {
		failure error
		want    error
	}{
		{context.DeadlineExceeded, domain.ErrMachineHostUnavailable},
		{domain.ErrMachineAccessDenied, domain.ErrMachineAccessDenied},
		{errors.New("synthetic observer failure"), domain.ErrMachineHostUnavailable},
	} {
		t.Run(tc.failure.Error(), func(t *testing.T) {
			inv, err := app.NewTrustedInventory(nil)
			if err != nil {
				t.Fatal(err)
			}
			err = app.RefreshLocalTrustedInventory(context.Background(), inv, observerFunc(func(context.Context) ([]domain.MachineObservation, error) {
				return nil, tc.failure
			}))
			if !errors.Is(err, tc.want) || !errors.Is(err, tc.failure) {
				t.Fatalf("local refresh = %v; want %v and cause %v", err, tc.want, tc.failure)
			}
		})
	}
}

func TestLocalRefreshPreservesRemoteInventoryAndRejectsCanceledObservation(t *testing.T) {
	inv, err := app.NewTrustedInventory([]app.HostEntry{host("host-a", "alpha.example", true)})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	remote := observed("host-a", "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", "remote-vm", now)
	local := observed(domain.LocalHostID, "bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb", "local-vm", now)
	for _, obs := range []domain.MachineObservation{remote, local} {
		if err := inv.ApplySnapshot(app.HostSnapshot{HostID: obs.HostID, Health: app.HostHealthObserved, Machines: []domain.MachineObservation{obs}}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err = app.RefreshLocalTrustedInventory(ctx, inv, observerFunc(func(context.Context) ([]domain.MachineObservation, error) {
		cancel()
		return []domain.MachineObservation{local}, nil
	}))
	if !errors.Is(err, domain.ErrMachineHostUnavailable) || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled observation refresh = %v", err)
	}
	if _, err := inv.ResolveMachine(local.Locator.String()); !errors.Is(err, domain.ErrMachineHostUnavailable) {
		t.Fatalf("canceled local identity = %v", err)
	}
	if err := app.RefreshLocalTrustedInventory(ctx, inv, observerFunc(func(context.Context) ([]domain.MachineObservation, error) {
		t.Error("pre-canceled refresh dispatched observer")
		return nil, nil
	})); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled refresh = %v", err)
	}
	if _, err := inv.ResolveMachine(remote.Locator.String()); err != nil {
		t.Fatalf("local refresh changed remote readiness: %v", err)
	}
	if err := app.RefreshLocalTrustedInventory(context.Background(), inv, observerFunc(func(context.Context) ([]domain.MachineObservation, error) {
		return []domain.MachineObservation{local}, nil
	})); err != nil {
		t.Fatal(err)
	}
	if _, err := inv.ResolveMachine(local.Locator.String()); err != nil {
		t.Fatalf("successful refresh did not restore local readiness: %v", err)
	}
}

func TestInventoryRefreshTimeoutsRespectRouteAndCallerBounds(t *testing.T) {
	for _, tc := range []struct {
		name        string
		host        app.HostEntry
		callerBound time.Duration
		want        time.Duration
	}{
		{"local default", app.HostEntry{ID: domain.LocalHostID, Address: "local", Enabled: true}, 0, time.Minute},
		{"remote default", app.HostEntry{ID: "host-a", Address: "alpha.example", Enabled: true}, 0, 15 * time.Second},
		{"explicit local", app.HostEntry{ID: domain.LocalHostID, Address: "local", Enabled: true, QueryTimeout: 2 * time.Second}, 0, 2 * time.Second},
		{"bounded local", app.HostEntry{ID: domain.LocalHostID, Address: "local", Enabled: true}, time.Second, time.Second},
		{"bounded remote", app.HostEntry{ID: "host-a", Address: "alpha.example", Enabled: true}, time.Second, time.Second},
		{"bounded explicit local", app.HostEntry{ID: domain.LocalHostID, Address: "local", Enabled: true, QueryTimeout: 2 * time.Second}, time.Second, time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inv, err := app.NewTrustedInventory([]app.HostEntry{tc.host})
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if tc.callerBound != 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.callerBound)
				defer cancel()
			}
			budgets := make(map[domain.HostID]time.Duration)
			_, err = app.RefreshTrustedInventory(ctx, inv, func(host app.HostEntry) app.TrustedHostObserver {
				return observerFunc(func(queryCtx context.Context) ([]domain.MachineObservation, error) {
					deadline, _ := queryCtx.Deadline()
					budgets[host.ID] = time.Until(deadline)
					return nil, nil
				})
			}, 1)
			if err != nil {
				t.Fatal(err)
			}
			remaining := budgets[tc.host.ID]
			if remaining > tc.want || remaining < tc.want-100*time.Millisecond {
				t.Errorf("query budget = %v; want bounded by %v", remaining, tc.want)
			}
		})
	}
}

func TestLocalRefreshDisabledAndMissingObserverFailClosed(t *testing.T) {
	inv, err := app.NewTrustedInventory([]app.HostEntry{disabledLocalHost()})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.RefreshLocalTrustedInventory(context.Background(), inv, nil); !errors.Is(err, domain.ErrMachineHostDisabled) {
		t.Fatalf("disabled refresh = %v", err)
	}
	if err := inv.ReplaceHosts(nil); err != nil {
		t.Fatal(err)
	}
	if err := app.RefreshLocalTrustedInventory(context.Background(), inv, nil); !errors.Is(err, domain.ErrMachineHostUnavailable) {
		t.Fatalf("nil observer refresh = %v", err)
	}
	if err := app.RefreshLocalTrustedInventory(context.Background(), nil, nil); err == nil {
		t.Fatal("nil inventory refresh succeeded")
	}
}
