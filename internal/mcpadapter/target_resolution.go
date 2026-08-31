package mcpadapter

import (
	"context"
	"fmt"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/backends/hyperv"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/Horcag/agent-machine-control/internal/target"
)

func (a *Adapter) getTargetService() (*app.TargetService, error) {
	if a.targetService != nil {
		return a.targetService, nil
	}
	if a.stateDir == "" {
		return nil, nil
	}
	sd, err := statedir.Resolve(a.stateDir)
	if err != nil {
		return nil, err
	}
	inventory, err := app.NewTrustedInventory(nil)
	if err != nil {
		return nil, err
	}
	store, err := target.NewStore(sd.TargetsDir())
	if err != nil {
		return nil, err
	}
	backend := hyperv.New()
	service, err := app.NewTargetService(inventory, store, app.WithTargetRefresh(func(ctx context.Context) error {
		_, refreshErr := app.RefreshTrustedInventory(ctx, inventory, func(host app.HostEntry) app.TrustedHostObserver {
			if host.ID != domain.LocalHostID {
				return nil
			}
			return backend
		}, 1)
		return refreshErr
	}))
	if err != nil {
		return nil, err
	}
	a.targetService = service
	return service, nil
}

func (a *Adapter) resolveTarget(ctx context.Context, reference string) (*app.TargetResolution, error) {
	service, err := a.getTargetService()
	if err != nil || service == nil {
		return nil, err
	}
	resolution, err := service.ResolveTarget(ctx, reference)
	if err != nil {
		return nil, err
	}
	return &resolution, nil
}

func (a *Adapter) observeTargetMachine(ctx context.Context, reference string, fallback MachineDTO) (MachineDTO, error) {
	resolution, err := a.resolveTarget(ctx, reference)
	if err != nil {
		return MachineDTO{}, err
	}
	if resolution == nil {
		return fallback, nil
	}
	observed, err := a.getDiscoveryService().Inspect(ctx, resolution.ProviderVMID)
	if err != nil {
		return MachineDTO{}, err
	}
	return convertToMachineDTO(observed), nil
}

func (a *Adapter) observeTargetCheckpoint(ctx context.Context, reference, checkpointID, fallbackName string) (CheckpointDTO, error) {
	resolution, err := a.resolveTarget(ctx, reference)
	if err != nil {
		return CheckpointDTO{}, err
	}
	if resolution == nil {
		return CheckpointDTO{ID: checkpointID, Name: fallbackName, VMID: reference, ObservationType: string(domain.ObservationInferred)}, nil
	}
	checkpoints, err := a.getRecoveryService().ListCheckpoints(ctx, resolution.ProviderVMID)
	if err != nil {
		return CheckpointDTO{}, err
	}
	for _, checkpoint := range checkpoints {
		if checkpoint.ID == checkpointID {
			return convertToCheckpointDTO(checkpoint), nil
		}
	}
	return CheckpointDTO{}, fmt.Errorf("observed checkpoint was not returned by provider")
}
