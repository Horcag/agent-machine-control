package app

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func (s *BootstrapService) stopEffect(ctx context.Context, spec BootstrapSpec) (bootstrapEffectOutcome, error) {
	_, err := s.requireExact(ctx, spec)
	if err != nil {
		if errors.Is(err, ErrBootstrapAbsent) {
			return bootstrapEffectOutcome{}, nil
		}
		return bootstrapEffectOutcome{}, err
	}
	healthy, err := s.daemonRequiresGracefulStop(ctx, spec)
	if err != nil {
		return bootstrapEffectOutcome{}, err
	}
	if healthy {
		graceful, gracefulErr := s.tryGracefulStop(ctx, spec)
		if gracefulErr != nil {
			return bootstrapEffectOutcome{}, gracefulErr
		}
		if graceful {
			return bootstrapEffectOutcome{}, nil
		}
	}
	return s.stopTaskFallback(ctx, spec)
}

func (s *BootstrapService) daemonRequiresGracefulStop(ctx context.Context, spec BootstrapSpec) (bool, error) {
	healthy, healthErr := s.daemon.Healthy(ctx, spec.StateDir)
	if healthErr == nil && healthy {
		return true, nil
	}
	if healthErr != nil && !errors.Is(healthErr, ErrBootstrapDrift) {
		return false, healthErr
	}
	release, err := s.daemon.ObserveRelease(ctx, spec.StateDir)
	if err != nil {
		return false, err
	}
	switch release.State {
	case BootstrapDaemonReleased, BootstrapDaemonShutdownPending,
		BootstrapDaemonEndpointUnavailable, BootstrapDaemonRetainedOwned:
		return false, nil
	case BootstrapDaemonHealthy:
		return true, nil
	case BootstrapDaemonReleaseDrift:
		return false, ErrBootstrapDrift
	default:
		return false, fmt.Errorf("%w: unknown daemon release state %q", ErrBootstrapDrift, release.State)
	}
}

func (s *BootstrapService) tryGracefulStop(ctx context.Context, spec BootstrapSpec) (bool, error) {
	stopErr := s.daemon.Stop(ctx, spec.StateDir)
	if errors.Is(stopErr, ErrBootstrapEndpointUnavailable) {
		return false, nil
	}
	if stopErr != nil {
		return false, stopErr
	}
	graceful, err := s.poll(ctx, s.stopGrace, func(pollCtx context.Context) (bool, error) {
		return s.bootstrapReleased(pollCtx, spec)
	})
	if err != nil {
		if errors.Is(err, ErrBootstrapDrift) {
			return false, err
		}
		return false, fmt.Errorf("%w: %w", ErrBootstrapUnhealthy, err)
	}
	return graceful, nil
}

func (s *BootstrapService) stopTaskFallback(ctx context.Context, spec BootstrapSpec) (bootstrapEffectOutcome, error) {
	outcome := bootstrapEffectOutcome{}
	// Re-read the complete fingerprint immediately before the effect. Runtime
	// release observations never authorize stopping a drifted task.
	obs, err := s.inspectExactOrAbsent(ctx, spec)
	if err != nil {
		return outcome, err
	}
	if obs.State != BootstrapAbsent && obs.TaskRunning {
		if err := s.adapter.StopTask(ctx, spec); err != nil {
			return outcome, err
		}
		outcome.taskStopApplied = true
	}
	if err := s.waitBootstrapReleased(ctx, spec); err != nil {
		return outcome, err
	}
	return outcome, nil
}

func (s *BootstrapService) bootstrapReleased(ctx context.Context, spec BootstrapSpec) (bool, error) {
	obs, err := s.inspectExactOrAbsent(ctx, spec)
	if err != nil {
		return false, err
	}
	release, err := s.daemon.ObserveRelease(ctx, spec.StateDir)
	if err != nil {
		return false, err
	}
	switch release.State {
	case BootstrapDaemonReleased:
		return obs.State == BootstrapAbsent || !obs.TaskRunning, nil
	case BootstrapDaemonHealthy, BootstrapDaemonShutdownPending, BootstrapDaemonEndpointUnavailable, BootstrapDaemonRetainedOwned:
		return false, nil
	case BootstrapDaemonReleaseDrift:
		return false, ErrBootstrapDrift
	default:
		return false, fmt.Errorf("%w: unknown daemon release state %q", ErrBootstrapDrift, release.State)
	}
}

func (s *BootstrapService) waitBootstrapReleased(ctx context.Context, spec BootstrapSpec) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return fmt.Errorf("%w: missing operation deadline", ErrBootstrapUnhealthy)
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return fmt.Errorf("%w: %w", ErrBootstrapUnhealthy, context.DeadlineExceeded)
	}
	released, err := s.poll(ctx, remaining, func(pollCtx context.Context) (bool, error) {
		return s.bootstrapReleased(pollCtx, spec)
	})
	if err != nil {
		if errors.Is(err, ErrBootstrapDrift) {
			return err
		}
		return fmt.Errorf("%w: %w", ErrBootstrapUnhealthy, err)
	}
	if !released {
		return ErrBootstrapUnhealthy
	}
	return nil
}
