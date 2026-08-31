package daemon

import (
	"context"
	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

type sshSafetyConfigLoader struct {
	provider guestssh.KeyProvider
}

func (l sshSafetyConfigLoader) GetMachineSafetyConfig(target domain.MachineRef) (*app.MachineSafetyConfig, error) {
	return l.GetMachineSafetyConfigContext(context.Background(), target)
}

func (l sshSafetyConfigLoader) GetMachineSafetyConfigContext(ctx context.Context, target domain.MachineRef) (*app.MachineSafetyConfig, error) {
	var config *guestssh.MachineSSHConfig
	var err error
	if provider, ok := l.provider.(interface {
		GetMachineConfigContext(context.Context, domain.MachineRef) (*guestssh.MachineSSHConfig, error)
	}); ok {
		config, err = provider.GetMachineConfigContext(ctx, target)
	} else {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		config, err = l.provider.GetMachineConfig(target)
	}
	if err != nil {
		return nil, err
	}
	return &app.MachineSafetyConfig{
		ExternalEffectsContained:    config.ExternalEffectsContained,
		RollbackCheckpointID:        config.RollbackCheckpointID,
		RequireProductionCheckpoint: config.RequireProductionCheckpoint,
	}, nil
}
