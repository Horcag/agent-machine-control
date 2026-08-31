package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

type contextualSafetyProvider struct{ guestssh.MockKeyProvider }

func (p *contextualSafetyProvider) GetMachineConfigContext(ctx context.Context, _ domain.MachineRef) (*guestssh.MachineSSHConfig, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return p.MachineConfig, nil
}

func TestSSHSafetyConfigLoaderUsesContextAwareProvider(t *testing.T) {
	provider := &contextualSafetyProvider{MockKeyProvider: guestssh.MockKeyProvider{MachineConfig: &guestssh.MachineSSHConfig{
		ExternalEffectsContained: true, RollbackCheckpointID: "e4a523d4-6b99-4d62-a5e2-4752c0f20001", RequireProductionCheckpoint: true,
	}}}
	loader := sshSafetyConfigLoader{provider: provider}
	config, err := loader.GetMachineSafetyConfig("c4a523d4-6b99-4d62-a5e2-4752c0f20001")
	if err != nil || !config.ExternalEffectsContained || !config.RequireProductionCheckpoint {
		t.Fatalf("config = %+v err %v", config, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := loader.GetMachineSafetyConfigContext(ctx, "c4a523d4-6b99-4d62-a5e2-4752c0f20001"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled config error = %v", err)
	}
}
