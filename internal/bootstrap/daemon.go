package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"

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
	record, present, err := readOwnedEndpoint(sd.DaemonDir())
	if err != nil {
		return false, err
	}
	if !present {
		return false, nil
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

func (d *LocalDaemon) ObserveRelease(ctx context.Context, stateDir string) (app.BootstrapDaemonReleaseObservation, error) {
	sd, err := statedir.Resolve(stateDir)
	if err != nil {
		return app.BootstrapDaemonReleaseObservation{}, err
	}
	record, err := daemon.ReadEndpointFile(sd.DaemonDir())
	if err == nil {
		return d.observeEndpointRelease(ctx, stateDir, sd.DaemonDir(), *record)
	}
	if !os.IsNotExist(err) {
		return daemonRelease(app.BootstrapDaemonReleaseDrift), nil
	}
	return d.observeSingletonRelease(sd.DaemonDir())
}

func (d *LocalDaemon) observeEndpointRelease(
	ctx context.Context,
	stateDir string,
	daemonDir string,
	record daemon.EndpointRecord,
) (app.BootstrapDaemonReleaseObservation, error) {
	ownershipState, err := d.endpointReleaseOwnershipState(daemonDir, record)
	if err != nil {
		return app.BootstrapDaemonReleaseObservation{}, err
	}
	if ownershipState != "" {
		return daemonRelease(ownershipState), nil
	}
	cl, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		if errors.Is(err, client.ErrDaemonUnavailable) {
			return daemonRelease(app.BootstrapDaemonEndpointUnavailable), nil
		}
		return daemonRelease(app.BootstrapDaemonReleaseDrift), nil
	}
	health, err := cl.Health(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return app.BootstrapDaemonReleaseObservation{}, err
		}
		if errors.Is(err, client.ErrDaemonUnavailable) {
			return daemonRelease(app.BootstrapDaemonEndpointUnavailable), nil
		}
		return daemonRelease(app.BootstrapDaemonReleaseDrift), nil
	}
	if health.PID != record.PID || cl.Endpoint() != record.Endpoint {
		return daemonRelease(app.BootstrapDaemonReleaseDrift), nil
	}
	return daemonRelease(app.BootstrapDaemonHealthy), nil
}

func (d *LocalDaemon) endpointReleaseOwnershipState(
	daemonDir string,
	record daemon.EndpointRecord,
) (app.BootstrapDaemonReleaseState, error) {
	runtimeID, _, _ := d.identity.CurrentIdentity()
	if record.RuntimeID == "" || record.RuntimeID != runtimeID || record.ProcessStartTime == "" {
		return app.BootstrapDaemonReleaseDrift, nil
	}
	alive, err := d.liveness.IsAlive(record.PID, record.ProcessStartTime)
	if err != nil {
		return "", err
	}
	owner, present, err := readSingletonOwner(daemonDir)
	if err != nil {
		return app.BootstrapDaemonReleaseDrift, nil
	}
	if present && !sameOwnedProcess(runtimeID, record.RuntimeID, record.PID, record.ProcessStartTime, owner.RuntimeID, owner.PID, owner.ProcessStartTime) {
		return app.BootstrapDaemonReleaseDrift, nil
	}
	if !present && alive {
		return app.BootstrapDaemonReleaseDrift, nil
	}
	if !alive {
		return app.BootstrapDaemonRetainedOwned, nil
	}
	return "", nil
}

func (d *LocalDaemon) observeSingletonRelease(daemonDir string) (app.BootstrapDaemonReleaseObservation, error) {
	owner, present, err := readSingletonOwner(daemonDir)
	if err != nil {
		return daemonRelease(app.BootstrapDaemonReleaseDrift), nil
	}
	if !present {
		return daemonRelease(app.BootstrapDaemonReleased), nil
	}
	runtimeID, _, _ := d.identity.CurrentIdentity()
	if owner.RuntimeID != runtimeID || owner.ProcessStartTime == "" {
		return daemonRelease(app.BootstrapDaemonReleaseDrift), nil
	}
	alive, err := d.liveness.IsAlive(owner.PID, owner.ProcessStartTime)
	if err != nil {
		return app.BootstrapDaemonReleaseObservation{}, err
	}
	if !alive {
		return daemonRelease(app.BootstrapDaemonRetainedOwned), nil
	}
	return daemonRelease(app.BootstrapDaemonShutdownPending), nil
}

func readSingletonOwner(daemonDir string) (*lease.LockOwnerRecord, bool, error) {
	lockDir := filepath.Join(daemonDir, "singleton.lock")
	info, err := os.Lstat(lockDir)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, true, app.ErrBootstrapDrift
	}
	owner, err := daemon.ReadSingletonOwner(daemonDir)
	if err != nil {
		return nil, true, err
	}
	return owner, true, nil
}

func sameOwnedProcess(runtimeID, endpointRuntimeID string, endpointPID int, endpointStartTime, ownerRuntimeID string, ownerPID int, ownerStartTime string) bool {
	return runtimeID != "" && endpointRuntimeID == runtimeID && ownerRuntimeID == runtimeID &&
		endpointPID > 0 && endpointPID == ownerPID && endpointStartTime != "" && endpointStartTime == ownerStartTime
}

func daemonRelease(state app.BootstrapDaemonReleaseState) app.BootstrapDaemonReleaseObservation {
	return app.BootstrapDaemonReleaseObservation{State: state}
}

func readOwnedEndpoint(daemonDir string) (daemon.EndpointRecord, bool, error) {
	record, err := daemon.ReadEndpointFile(daemonDir)
	if err == nil {
		return *record, true, nil
	}
	if !os.IsNotExist(err) {
		return daemon.EndpointRecord{}, false, app.ErrBootstrapDrift
	}
	if _, lockErr := os.Lstat(filepath.Join(daemonDir, "singleton.lock")); !os.IsNotExist(lockErr) {
		return daemon.EndpointRecord{}, false, app.ErrBootstrapDrift
	}
	return daemon.EndpointRecord{}, false, nil
}

func (d *LocalDaemon) Stop(ctx context.Context, stateDir string) error {
	cl, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		if errors.Is(err, client.ErrDaemonUnavailable) {
			return app.ErrBootstrapEndpointUnavailable
		}
		if errors.Is(err, client.ErrDenied) || errors.Is(err, client.ErrMalformedResponse) {
			return app.ErrBootstrapDrift
		}
		return err
	}
	_, err = cl.StopDaemon(ctx)
	if errors.Is(err, client.ErrDaemonUnavailable) {
		return app.ErrBootstrapEndpointUnavailable
	}
	if errors.Is(err, client.ErrDenied) || errors.Is(err, client.ErrMalformedResponse) {
		return app.ErrBootstrapDrift
	}
	return err
}
