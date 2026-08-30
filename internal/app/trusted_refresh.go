package app

import (
	"context"
	"errors"
	"sync"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

// TrustedHostObserver lists machines for one trusted host.
type TrustedHostObserver interface {
	ListMachines(ctx context.Context) ([]domain.MachineObservation, error)
}

// HostObserverFactory creates a host-scoped observer for refresh.
type HostObserverFactory func(HostEntry) TrustedHostObserver

type refreshResult struct {
	index    int
	snapshot HostSnapshot
}

// RefreshTrustedInventory refreshes all enabled hosts with bounded concurrency.
func RefreshTrustedInventory(ctx context.Context, inv *TrustedInventory, factory HostObserverFactory, concurrency int) ([]HostSnapshot, error) {
	if err := validateRefreshInputs(inv, factory); err != nil {
		return nil, err
	}
	if concurrency <= 0 {
		concurrency = 1
	}
	snapshots, enabled, err := applyDisabledHosts(inv)
	if err != nil {
		return nil, err
	}
	ordered := refreshEnabledHosts(ctx, enabled, factory, concurrency)
	return applyRefreshResults(ctx, inv, snapshots, ordered)
}

func validateRefreshInputs(inv *TrustedInventory, factory HostObserverFactory) error {
	if inv == nil {
		return errors.New("app: no trusted inventory configured")
	}
	if factory == nil {
		return errors.New("app: no trusted host observer factory configured")
	}
	return nil
}

func applyDisabledHosts(inv *TrustedInventory) ([]HostSnapshot, []HostEntry, error) {
	hosts := inv.Hosts()
	snapshots := make([]HostSnapshot, 0, len(hosts))
	enabled := make([]HostEntry, 0, len(hosts))
	for _, host := range hosts {
		if host.Enabled {
			enabled = append(enabled, host)
			continue
		}
		snapshot := HostSnapshot{HostID: host.ID, Health: HostHealthStale}
		if err := inv.ApplySnapshot(snapshot); err != nil {
			return nil, nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, enabled, nil
}

func refreshEnabledHosts(ctx context.Context, hosts []HostEntry, factory HostObserverFactory, concurrency int) []HostSnapshot {
	jobs := make(chan int)
	results := make(chan refreshResult, len(hosts))
	var wg sync.WaitGroup
	workerCount := min(concurrency, len(hosts))
	for range workerCount {
		wg.Go(func() {
			for idx := range jobs {
				results <- refreshResult{index: idx, snapshot: refreshOneHost(ctx, hosts[idx], factory)}
			}
		})
	}
	go func() {
		defer close(jobs)
		for idx := range hosts {
			if ctx.Err() != nil {
				fillUnavailableSnapshots(results, hosts, idx, ctx.Err())
				return
			}
			select {
			case <-ctx.Done():
				fillUnavailableSnapshots(results, hosts, idx, ctx.Err())
				return
			case jobs <- idx:
			}
		}
	}()
	wg.Wait()
	close(results)
	ordered := make([]HostSnapshot, len(hosts))
	for res := range results {
		ordered[res.index] = res.snapshot
	}
	return ordered
}

func fillUnavailableSnapshots(results chan<- refreshResult, hosts []HostEntry, start int, err error) {
	for idx := start; idx < len(hosts); idx++ {
		results <- refreshResult{
			index: idx,
			snapshot: HostSnapshot{
				HostID: hosts[idx].ID,
				Health: HostHealthUnavailable,
				Err:    err,
			},
		}
	}
}

func refreshOneHost(ctx context.Context, host HostEntry, factory HostObserverFactory) HostSnapshot {
	hostCtx, cancel := context.WithTimeout(ctx, host.effectiveQueryTimeout())
	observer := factory(host)
	if observer == nil {
		cancel()
		return HostSnapshot{HostID: host.ID, Health: HostHealthUnavailable, Err: errors.New("app: nil trusted host observer")}
	}
	machines, err := observer.ListMachines(hostCtx)
	health := classifyRefreshHealth(hostCtx, err)
	cancel()
	return HostSnapshot{
		HostID:   host.ID,
		Health:   health,
		Machines: machines,
		Err:      err,
	}
}

func applyRefreshResults(ctx context.Context, inv *TrustedInventory, snapshots, ordered []HostSnapshot) ([]HostSnapshot, error) {
	for _, snapshot := range ordered {
		if snapshot.HostID == "" {
			continue
		}
		if err := inv.ApplySnapshot(snapshot); err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	if ctx.Err() != nil {
		return snapshots, ctx.Err()
	}
	return snapshots, nil
}

func classifyRefreshHealth(ctx context.Context, err error) HostHealth {
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return HostHealthUnavailable
	}
	if err == nil {
		return HostHealthObserved
	}
	switch {
	case errors.Is(err, domain.ErrMachineAccessDenied):
		return HostHealthAccessDenied
	case errors.Is(err, domain.ErrMachineHostUnavailable):
		return HostHealthUnavailable
	default:
		return HostHealthUnavailable
	}
}
