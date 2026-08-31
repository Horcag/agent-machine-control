package sessions

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

var errPendingCleanupIncomplete = errors.New("sessions: pending channel cleanup incomplete")

type pendingCleanup struct {
	channel guestssh.Channel
	lane    chan struct{}
}

// WithCleanupTimeout configures the bound for channel cleanup attempts.
func WithCleanupTimeout(timeout time.Duration) ManagerOption {
	return func(m *Manager) {
		if timeout > 0 {
			m.cleanupTimeout = timeout
		}
	}
}

func (m *Manager) beginOpen() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return domain.ErrSessionManagerClosed
	}
	if m.activeOpens == 0 {
		m.opensDrained = make(chan struct{})
	}
	m.activeOpens++
	return nil
}

func (m *Manager) endOpen() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activeOpens--
	if m.activeOpens == 0 {
		close(m.opensDrained)
		m.opensDrained = nil
	}
}

// OpenFailure records transport-effect truth when session publication fails after channel creation.
type OpenFailure struct {
	Cause           error
	ChannelCreated  bool
	CleanupComplete bool
	CleanupErr      error
}

func (e *OpenFailure) Error() string {
	return errors.Join(e.Cause, e.CleanupErr).Error()
}

// Unwrap preserves both the publication failure and cleanup outcome for callers.
func (e *OpenFailure) Unwrap() []error {
	causes := make([]error, 0, 2)
	if e.Cause != nil {
		causes = append(causes, e.Cause)
	}
	if e.CleanupErr != nil {
		causes = append(causes, e.CleanupErr)
	}
	return causes
}

func (m *Manager) cleanupFailedOpen(parent context.Context, channel guestssh.Channel, cause error) error {
	cleanupCtx, cancel := m.openCleanupContext(parent)
	defer cancel()
	complete, cleanupErr := closeChannel(cleanupCtx, channel)
	if cleanupErr == io.EOF {
		cleanupErr = nil
	}
	if !complete {
		m.supervisePendingCleanup(channel)
	}
	return &OpenFailure{
		Cause:           cause,
		ChannelCreated:  true,
		CleanupComplete: complete,
		CleanupErr:      cleanupErr,
	}
}

func (m *Manager) openCleanupContext(parent context.Context) (context.Context, context.CancelFunc) {
	timeout := m.cleanupTimeout
	if deadline, ok := parent.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return context.WithDeadline(context.Background(), deadline)
		}
		if remaining < timeout {
			timeout = remaining
		}
	}
	return context.WithTimeout(context.Background(), timeout)
}

func (m *Manager) supervisePendingCleanup(channel guestssh.Channel) {
	cleanup := &pendingCleanup{channel: channel, lane: make(chan struct{}, 1)}
	m.mu.Lock()
	m.nextCleanupID++
	id := m.nextCleanupID
	m.pendingCleanups[id] = cleanup
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), m.cleanupTimeout)
		defer cancel()
		_ = m.retryPendingCleanup(ctx, id, cleanup, true)
	}()
}

func (m *Manager) retryPendingCleanup(ctx context.Context, id uint64, cleanup *pendingCleanup, supervised bool) error {
	if err := acquireSessionLane(ctx, cleanup.lane); err != nil {
		return errors.Join(errPendingCleanupIncomplete, err)
	}
	defer func() { <-cleanup.lane }()

	m.mu.RLock()
	current, exists := m.pendingCleanups[id]
	closed := m.closed
	m.mu.RUnlock()
	if !exists || current != cleanup {
		return nil
	}
	if supervised && closed {
		return nil
	}

	complete, cleanupErr := closeChannel(ctx, cleanup.channel)
	if cleanupErr == io.EOF {
		cleanupErr = nil
	}
	if !complete {
		return errors.Join(errPendingCleanupIncomplete, cleanupErr)
	}

	m.mu.Lock()
	if m.pendingCleanups[id] == cleanup {
		delete(m.pendingCleanups, id)
	}
	m.mu.Unlock()
	return cleanupErr
}
