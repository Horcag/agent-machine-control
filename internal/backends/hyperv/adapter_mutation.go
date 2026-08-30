package hyperv

import (
	"context"
	"errors"
	"fmt"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

// Capabilities returns the capability set supported by the Hyper-V adapter for the target.
func (a *Adapter) Capabilities(_ context.Context, _ string) (domain.CapabilitySet, error) {
	route, err := a.route.validated()
	if err != nil {
		return nil, err
	}
	if route.Remote {
		return domain.ReadOnlyMachineCapabilities(), nil
	}
	return domain.DirectMachineCapabilities(), nil
}

func (a *Adapter) rejectRemotePrivilegedRoute() error {
	route, err := a.route.validated()
	if err != nil {
		return err
	}
	if route.Remote {
		return ErrRemoteRouteReadOnly
	}
	return nil
}

func (a *Adapter) executeMachineMutation(ctx context.Context, id string, script string, env []string) (domain.MachineObservation, error) {
	now := a.now()
	if err := domain.ValidateMachineGUID(id); err != nil {
		return domain.MachineObservation{}, err
	}
	if err := a.rejectRemotePrivilegedRoute(); err != nil {
		return domain.MachineObservation{}, err
	}
	stdout, err := a.executeScript(ctx, script, env)
	if err != nil {
		return domain.MachineObservation{}, err
	}
	return parseMutationResponse(stdout, now)
}

func (a *Adapter) executeScript(ctx context.Context, script string, env []string) ([]byte, error) {
	exe, err := a.resolveExecutable()
	if err != nil {
		return nil, ErrExecutableNotFound
	}

	args := []string{"-NoProfile", "-NonInteractive", "-NoLogo", "-OutputFormat", "Text", "-Command", script}
	stdout, _, runErr := a.executor.Execute(ctx, exe, args, env)

	if errors.Is(runErr, ErrOutputExceededLimit) {
		return nil, ErrOutputExceededLimit
	}
	if ctx.Err() != nil || errors.Is(runErr, ErrCommandTimeout) {
		return nil, fmt.Errorf("%w: %w", ErrCommandTimeout, ctx.Err())
	}
	if runErr != nil {
		return nil, ErrHostUnavailable
	}
	if len(stdout) == 0 {
		return nil, ErrMalformedResponse
	}
	return stdout, nil
}

// StartMachine starts a virtual machine by GUID and returns the resulting machine observation.
func (a *Adapter) StartMachine(ctx context.Context, id string) (domain.MachineObservation, error) {
	env := []string{fmt.Sprintf("%s=%s", TargetVMIDEnvVar, id)}
	return a.executeMachineMutation(ctx, id, ScriptStart, env)
}

// StopMachine stops a virtual machine by GUID with mode (shutdown, save, turn-off).
func (a *Adapter) StopMachine(ctx context.Context, id string, mode string) (domain.MachineObservation, error) {
	env := []string{
		fmt.Sprintf("%s=%s", TargetVMIDEnvVar, id),
		fmt.Sprintf("%s=%s", StopModeEnvVar, mode),
	}
	return a.executeMachineMutation(ctx, id, ScriptStop, env)
}

// ListCheckpoints lists all snapshots/checkpoints for a virtual machine.
func (a *Adapter) ListCheckpoints(ctx context.Context, id string) ([]domain.CheckpointObservation, error) {
	now := a.now()
	if err := domain.ValidateMachineGUID(id); err != nil {
		return nil, err
	}
	if err := a.rejectRemotePrivilegedRoute(); err != nil {
		return nil, err
	}

	env := []string{fmt.Sprintf("%s=%s", TargetVMIDEnvVar, id)}
	stdout, err := a.executeScript(ctx, ScriptCheckpointList, env)
	if err != nil {
		return nil, err
	}

	return parseCheckpointListResponse(stdout, now)
}

// CreateCheckpoint creates a new snapshot/checkpoint for a virtual machine.
func (a *Adapter) CreateCheckpoint(ctx context.Context, id string, name string) (domain.CheckpointObservation, error) {
	now := a.now()
	if err := domain.ValidateMachineGUID(id); err != nil {
		return domain.CheckpointObservation{}, err
	}
	if err := a.rejectRemotePrivilegedRoute(); err != nil {
		return domain.CheckpointObservation{}, err
	}

	env := []string{
		fmt.Sprintf("%s=%s", TargetVMIDEnvVar, id),
		fmt.Sprintf("%s=%s", SnapshotNameEnvVar, name),
	}
	stdout, err := a.executeScript(ctx, ScriptCheckpointCreate, env)
	if err != nil {
		return domain.CheckpointObservation{}, err
	}

	return parseCheckpointCreateResponse(stdout, now)
}

// RestoreCheckpoint restores a virtual machine to an exact snapshot GUID.
func (a *Adapter) RestoreCheckpoint(ctx context.Context, id string, checkpointID string) (domain.MachineObservation, error) {
	if err := domain.ValidateMachineGUID(checkpointID); err != nil {
		return domain.MachineObservation{}, err
	}

	env := []string{
		fmt.Sprintf("%s=%s", TargetVMIDEnvVar, id),
		fmt.Sprintf("%s=%s", SnapshotIDEnvVar, checkpointID),
	}
	return a.executeMachineMutation(ctx, id, ScriptCheckpointRestore, env)
}
