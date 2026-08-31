package mcpadapter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/backends/hyperv"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/Horcag/agent-machine-control/internal/target"
)

var errProtectedTargetUnavailable = errors.New("mcp: protected target state is unavailable")

func (a *Adapter) getTargetService() (*app.TargetService, error) {
	a.targetServiceMu.Lock()
	defer a.targetServiceMu.Unlock()

	if a.targetService != nil {
		return a.targetService, nil
	}
	if a.targetServiceInitialized {
		return nil, a.targetServiceErr
	}
	a.targetServiceInitialized = true
	if a.allowUnscopedTestTargetFallback {
		return nil, nil
	}
	if strings.TrimSpace(a.stateDir) == "" {
		a.targetServiceErr = errProtectedTargetUnavailable
		return nil, a.targetServiceErr
	}
	sd, err := statedir.Resolve(a.stateDir)
	if err != nil {
		a.targetServiceErr = fmt.Errorf("%w: %w", errProtectedTargetUnavailable, err)
		return nil, a.targetServiceErr
	}
	info, err := os.Stat(sd.Root())
	if err != nil || !info.IsDir() {
		a.targetServiceErr = errProtectedTargetUnavailable
		return nil, a.targetServiceErr
	}
	inventory, err := app.NewTrustedInventory(nil)
	if err != nil {
		a.targetServiceErr = fmt.Errorf("%w: %w", errProtectedTargetUnavailable, err)
		return nil, a.targetServiceErr
	}
	store, err := target.NewStore(sd.TargetsDir())
	if err != nil {
		a.targetServiceErr = fmt.Errorf("%w: %w", errProtectedTargetUnavailable, err)
		return nil, a.targetServiceErr
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
		a.targetServiceErr = fmt.Errorf("%w: %w", errProtectedTargetUnavailable, err)
		return nil, a.targetServiceErr
	}
	a.targetService = service
	return service, nil
}

func (a *Adapter) resolveTarget(ctx context.Context, reference string) (*app.TargetResolution, error) {
	service, err := a.getTargetService()
	if err != nil {
		return nil, err
	}
	if service == nil {
		if a.allowUnscopedTestTargetFallback {
			return nil, nil
		}
		return nil, errProtectedTargetUnavailable
	}
	resolution, err := service.ResolveTarget(ctx, reference)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errProtectedTargetUnavailable, err)
	}
	return &resolution, nil
}

func (a *Adapter) resolveMutationTarget(ctx context.Context, targetID, reason, idempotencyKey string) (string, error) {
	if targetID != "" && strings.TrimSpace(targetID) != targetID {
		return "", NewInputError("invalid target reference")
	}
	if err := domain.ValidateReason(reason); err != nil {
		return "", NewInputError("invalid reason")
	}
	if err := domain.ValidateIdempotencyKey(idempotencyKey); err != nil {
		return "", NewInputError("invalid idempotency key")
	}
	resolution, err := a.resolveTarget(ctx, targetID)
	if err != nil {
		return "", err
	}
	if resolution != nil {
		return resolution.Locator.String(), nil
	}
	if err := domain.ValidateMachineGUID(targetID); err != nil {
		return "", NewInputError("invalid target GUID")
	}
	return targetID, nil
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
