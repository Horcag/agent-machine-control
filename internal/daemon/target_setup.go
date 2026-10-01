package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/Horcag/agent-machine-control/internal/target"
)

func initializeTargetSubsystem(
	sd *statedir.StateDir,
	backend app.Backend,
	auditStore *audit.Store,
	receiptStore *receipt.Store,
	approvalStore *approval.Store,
	clock func() time.Time,
) (*app.TargetService, *app.TargetCoordinator, error) {
	inventory, err := app.NewTrustedInventory(nil)
	if err != nil {
		return nil, nil, fmt.Errorf("daemon: failed to initialize trusted inventory: %w", err)
	}
	targetStore, err := target.NewStore(sd.TargetsDir())
	if err != nil {
		return nil, nil, fmt.Errorf("daemon: failed to initialize target authority: %w", err)
	}
	refreshTarget := func(ctx context.Context) error {
		return app.RefreshLocalTrustedInventory(ctx, inventory, backend)
	}
	targetService, err := app.NewTargetService(inventory, targetStore, app.WithTargetRefresh(refreshTarget), app.WithTargetObserver(backend))
	if err != nil {
		return nil, nil, fmt.Errorf("daemon: failed to initialize target service: %w", err)
	}
	targetJournal, err := target.NewMutationJournal(sd.TargetsDir())
	if err != nil {
		return nil, nil, fmt.Errorf("daemon: failed to initialize target mutation journal: %w", err)
	}
	coordinator, err := app.NewTargetCoordinator(targetService, targetJournal, auditStore, receiptStore, approvalStore, app.WithTargetCoordinatorClock(clock))
	if err != nil {
		return nil, nil, fmt.Errorf("daemon: failed to initialize target coordinator: %w", err)
	}
	if _, err := coordinator.ReconcileStartup(context.Background()); err != nil {
		return nil, nil, fmt.Errorf("daemon: failed to reconcile target mutations: %w", err)
	}
	return targetService, coordinator, nil
}
