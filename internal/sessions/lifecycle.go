package sessions

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

const sessionCleanupTimeout = 5 * time.Second

type finalizationSource uint8

const (
	finalizationExplicitClose finalizationSource = iota
	finalizationNaturalExit
	finalizationShutdown
)

func acquireSessionCloseLane(ctx context.Context, s *Session) error {
	return acquireSessionLane(ctx, s.closeSem)
}

func acquireSessionLane(ctx context.Context, lane chan struct{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case lane <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-lane
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func releaseSessionCloseLane(s *Session) {
	<-s.closeSem
}

func closeChannel(ctx context.Context, channel guestssh.Channel) (bool, error) {
	callErr := channel.Close(ctx)
	outcome := channel.LastCloseOutcome()
	if outcome.Complete {
		return true, outcome.Err
	}
	return false, callErr
}

func sessionTerminalError(s *Session, obs domain.SessionObservation) error {
	if s.terminalErr != nil {
		return s.terminalErr
	}
	if obs.State != domain.SessionStateClosed && obs.ErrorMessage != "" {
		return errors.New(obs.ErrorMessage)
	}
	return nil
}

func (m *Manager) finalizeSession(
	ctx context.Context,
	s *Session,
	source finalizationSource,
	exitCode *int,
	waitErr error,
	force bool,
) (*domain.SessionObservation, error) {
	s.mu.Lock()
	if s.obs.State.IsTerminal() {
		obs := s.obs
		err := sessionTerminalError(s, obs)
		s.mu.Unlock()
		return &obs, err
	}
	s.obs.State = domain.SessionStateClosing
	s.mu.Unlock()

	closeComplete, closeErr := closeChannel(ctx, s.channel)
	if closeErr == io.EOF {
		closeErr = nil
	}

	now := m.now()
	s.mu.Lock()
	switch source {
	case finalizationNaturalExit:
		finalizeNaturalExit(s, now, exitCode, waitErr, closeComplete, closeErr)
	case finalizationShutdown:
		finalizeShutdown(s, now, closeComplete, closeErr)
	case finalizationExplicitClose:
		finalizeExplicitClose(s, now, closeComplete, closeErr, force)
	}
	obs := s.obs
	terminal := obs.State.IsTerminal()
	terminalErr := sessionTerminalError(s, obs)
	s.mu.Unlock()

	if terminal {
		s.closeOnce.Do(func() { close(s.closedCh) })
	}
	persistErr := m.persistSession(s).Err
	return &obs, errors.Join(terminalErr, persistErr)
}

func finalizeNaturalExit(s *Session, now time.Time, exitCode *int, waitErr error, closeComplete bool, closeErr error) {
	s.obs.ExitCode = exitCode
	s.naturalWaitErr = waitErr
	s.terminalErr = errors.Join(waitErr, closeErr)
	if !closeComplete {
		s.obs.State = domain.SessionStateClosing
		s.obs.ClosedAt = nil
		s.obs.ErrorMessage = "transport_cleanup_incomplete"
		return
	}
	s.closed = true
	s.obs.ClosedAt = &now
	switch {
	case waitErr != nil && closeErr != nil:
		s.obs.State = domain.SessionStateFailed
		s.obs.ErrorMessage = "transport_wait_and_cleanup_failed"
	case waitErr != nil:
		s.obs.State = domain.SessionStateFailed
		s.obs.ErrorMessage = "transport_wait_failed"
	case !closeComplete || closeErr != nil:
		s.obs.State = domain.SessionStateFailed
		s.obs.ErrorMessage = "transport_cleanup_failed"
	default:
		s.obs.State = domain.SessionStateClosed
		s.obs.ErrorMessage = ""
	}
}

func finalizeShutdown(s *Session, now time.Time, closeComplete bool, closeErr error) {
	if !closeComplete {
		s.terminalErr = errors.Join(s.naturalWaitErr, closeErr)
		s.obs.State = domain.SessionStateClosing
		s.obs.ClosedAt = nil
		s.obs.ErrorMessage = "transport_cleanup_incomplete"
		return
	}
	s.closed = true
	s.obs.ClosedAt = &now
	s.terminalErr = errors.Join(s.naturalWaitErr, closeErr)
	switch {
	case s.naturalWaitErr != nil && closeErr != nil:
		s.obs.State = domain.SessionStateFailed
		s.obs.ErrorMessage = "transport_wait_and_cleanup_failed"
	case s.naturalWaitErr != nil:
		s.obs.State = domain.SessionStateFailed
		s.obs.ErrorMessage = "transport_wait_failed"
	case closeComplete && closeErr == nil:
		s.obs.State = domain.SessionStateClosed
		s.obs.ErrorMessage = ""
	default:
		s.obs.State = domain.SessionStateFailed
		s.obs.ErrorMessage = "transport_close_failed"
	}
}

func finalizeExplicitClose(s *Session, now time.Time, closeComplete bool, closeErr error, _ bool) {
	s.terminalErr = errors.Join(s.naturalWaitErr, closeErr)
	if !closeComplete {
		s.obs.State = domain.SessionStateClosing
		s.obs.ClosedAt = nil
		s.obs.ErrorMessage = "transport_cleanup_incomplete"
		return
	}
	s.closed = true
	s.obs.ClosedAt = &now
	switch {
	case s.naturalWaitErr != nil && closeErr != nil:
		s.obs.State = domain.SessionStateFailed
		s.obs.ErrorMessage = "transport_wait_and_cleanup_failed"
	case s.naturalWaitErr != nil:
		s.obs.State = domain.SessionStateFailed
		s.obs.ErrorMessage = "transport_wait_failed"
	case closeComplete && closeErr == nil:
		s.obs.State = domain.SessionStateClosed
		s.obs.ErrorMessage = ""
	default:
		s.obs.State = domain.SessionStateFailed
		s.obs.ErrorMessage = "transport_close_failed"
	}
}
