package sessions

import (
	"context"
	"errors"
	"maps"
)

// Shutdown cleanly terminates all active sessions.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	opensDrained := m.opensDrained
	m.mu.Unlock()

	if opensDrained != nil {
		select {
		case <-opensDrained:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	m.mu.RLock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	pending := make(map[uint64]*pendingCleanup, len(m.pendingCleanups))
	maps.Copy(pending, m.pendingCleanups)
	m.mu.RUnlock()

	var errs []error
	for _, session := range sessions {
		if err := acquireSessionCloseLane(ctx, session); err != nil {
			errs = append(errs, ctx.Err())
			continue
		}
		_, closeErr := m.finalizeSession(ctx, session, finalizationShutdown, nil, nil)
		if closeErr != nil {
			errs = append(errs, closeErr)
		}
		releaseSessionCloseLane(session)
	}
	for id, cleanup := range pending {
		if err := m.retryPendingCleanup(ctx, id, cleanup, false); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Drained reports whether no open attempt, active transport, or supervised cleanup remains.
func (m *Manager) Drained() bool {
	if m == nil {
		return true
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.activeOpens != 0 || len(m.pendingCleanups) != 0 {
		return false
	}
	for _, session := range m.sessions {
		session.mu.RLock()
		terminal := session.obs.State.IsTerminal()
		session.mu.RUnlock()
		if !terminal {
			return false
		}
	}
	return true
}
