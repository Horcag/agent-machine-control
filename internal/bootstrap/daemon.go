package bootstrap

import (
	"context"
	"errors"
	"os"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

type LocalDaemon struct {
	liveness lease.LivenessChecker
	identity lease.IdentityProvider
}

func NewLocalDaemon() *LocalDaemon {
	return &LocalDaemon{liveness: &lease.DefaultLivenessChecker{}, identity: &lease.DefaultIdentityProvider{}}
}

func (d *LocalDaemon) Healthy(ctx context.Context, stateDir string) (bool, error) {
	sd, err := statedir.Resolve(stateDir)
	if err != nil {
		return false, err
	}
	record, err := daemon.ReadEndpointFile(sd.DaemonDir())
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, app.ErrBootstrapDrift
	}
	runtimeID, _, _ := d.identity.CurrentIdentity()
	if record.RuntimeID == "" || record.RuntimeID != runtimeID || record.ProcessStartTime == "" {
		return false, app.ErrBootstrapDrift
	}
	alive, err := d.liveness.IsAlive(record.PID, record.ProcessStartTime)
	if err != nil || !alive {
		if err != nil {
			return false, err
		}
		return false, app.ErrBootstrapDrift
	}
	cl, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		return false, app.ErrBootstrapDrift
	}
	health, err := cl.Health(ctx)
	if err != nil {
		return false, app.ErrBootstrapDrift
	}
	if health.PID != record.PID || cl.Endpoint() != record.Endpoint {
		return false, app.ErrBootstrapDrift
	}
	return true, nil
}

func (d *LocalDaemon) Stop(ctx context.Context, stateDir string) error {
	cl, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		if errors.Is(err, client.ErrDaemonUnavailable) {
			return nil
		}
		return err
	}
	_, err = cl.StopDaemon(ctx)
	return err
}
