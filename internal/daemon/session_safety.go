package daemon

import (
	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

type sshSafetyConfigLoader struct {
	provider guestssh.KeyProvider
}

func (l sshSafetyConfigLoader) GetMachineSafetyConfig(target domain.MachineRef) (*app.MachineSafetyConfig, error) {
	config, err := l.provider.GetMachineConfig(target)
	if err != nil {
		return nil, err
	}
	return &app.MachineSafetyConfig{
		ExternalEffectsContained:    config.ExternalEffectsContained,
		RollbackCheckpointID:        config.RollbackCheckpointID,
		RequireProductionCheckpoint: config.RequireProductionCheckpoint,
	}, nil
}
