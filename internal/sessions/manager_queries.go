package sessions

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

// Wait blocks for quiet settle time or regex pattern match against output.
func (m *Manager) Wait(ctx context.Context, id domain.SessionID, caller domain.ActorContext, settle time.Duration, regexStr string, afterSeq uint64, timeout time.Duration) ([]domain.SessionChunk, uint64, uint64, bool, *domain.SessionObservation, error) {
	if !caller.HasScope(domain.ScopeSessionRead) {
		return nil, 0, 0, false, nil, domain.ErrSessionAccessDenied
	}
	if err := domain.ValidateSessionID(string(id)); err != nil {
		return nil, 0, 0, false, nil, err
	}

	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if !ok || !m.authorize(caller, s) {
		return nil, 0, 0, false, nil, domain.ErrSessionNotFound
	}

	if regexStr != "" {
		chunks, nextSeq, lossBytes, matched, err := WaitRegex(ctx, s.buffer, regexStr, afterSeq, timeout)
		s.mu.Lock()
		s.obs.LastActivityAt = m.now()
		obs := s.obs
		s.mu.Unlock()
		return chunks, nextSeq, lossBytes, matched, &obs, err
	}

	chunks, nextSeq, lossBytes, err := WaitSettle(ctx, s.buffer, settle, afterSeq, timeout)
	s.mu.Lock()
	s.obs.LastActivityAt = m.now()
	obs := s.obs
	s.mu.Unlock()
	return chunks, nextSeq, lossBytes, true, &obs, err
}

func (m *Manager) loadDiskSession(id domain.SessionID, caller domain.ActorContext) (*domain.SessionObservation, bool, error) {
	if err := domain.ValidateSessionID(string(id)); err != nil {
		return nil, false, err
	}
	if m.sessionsDir == "" {
		return nil, false, nil
	}
	obs, err := readSessionState(m.sessionsDir, id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if string(caller.EffectiveActor) != string(obs.OwnerActor) && !caller.HasScope(domain.ScopeSessionAdmin) {
		return nil, false, nil
	}
	return obs, true, nil
}

func (m *Manager) loadDiskSessions(target domain.MachineRef, caller domain.ActorContext, seen map[domain.SessionID]bool) ([]domain.SessionObservation, error) {
	if m.sessionsDir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(m.sessionsDir)
	if err != nil {
		return nil, err
	}

	var results []domain.SessionObservation
	for _, entry := range entries {
		id, valid := sessionIDFromStateFilename(entry.Name())
		if !valid {
			continue
		}
		obs, ok, err := m.loadDiskSession(id, caller)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if seen[id] {
			continue
		}
		if !sessionTargetMatchesFilter(obs.Target, target) {
			continue
		}
		seen[id] = true
		results = append(results, *obs)
	}
	return results, nil
}

// List returns observations of all accessible sessions for the caller.
func (m *Manager) List(_ context.Context, caller domain.ActorContext, target domain.MachineRef) ([]domain.SessionObservation, error) {
	if !caller.HasScope(domain.ScopeSessionRead) {
		return nil, domain.ErrSessionAccessDenied
	}
	seen := make(map[domain.SessionID]bool)
	var result []domain.SessionObservation

	m.mu.RLock()
	for _, s := range m.sessions {
		if !sessionTargetMatchesFilter(s.obs.Target, target) {
			continue
		}
		if !m.authorize(caller, s) {
			continue
		}
		s.mu.RLock()
		obs := s.obs
		s.mu.RUnlock()
		seen[obs.ID] = true
		result = append(result, obs)
	}
	m.mu.RUnlock()

	diskSessions, err := m.loadDiskSessions(target, caller, seen)
	if err != nil {
		return nil, err
	}
	result = append(result, diskSessions...)

	return result, nil
}

func sessionTargetMatchesFilter(sessionTarget, filter domain.MachineRef) bool {
	if filter == "" || sessionTarget == filter {
		return true
	}
	locator, err := domain.ParseMachineLocator(string(sessionTarget))
	return err == nil && locator.HostID == domain.LocalHostID && locator.VMID == string(filter)
}

// Get returns the current observation of a session.
func (m *Manager) Get(_ context.Context, id domain.SessionID, caller domain.ActorContext) (*domain.SessionObservation, error) {
	if !caller.HasScope(domain.ScopeSessionRead) {
		return nil, domain.ErrSessionAccessDenied
	}
	if err := domain.ValidateSessionID(string(id)); err != nil {
		return nil, err
	}

	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if ok {
		if !m.authorize(caller, s) {
			return nil, domain.ErrSessionNotFound
		}
		s.mu.RLock()
		obs := s.obs
		s.mu.RUnlock()
		return &obs, nil
	}

	if obs, found, err := m.loadDiskSession(id, caller); err != nil {
		return nil, err
	} else if found {
		return obs, nil
	}

	return nil, domain.ErrSessionNotFound
}

// MutationTarget resolves only the target needed to authorize a session mutation.
// It enforces ownership without requiring or disclosing session:read metadata.
func (m *Manager) MutationTarget(id domain.SessionID, caller domain.ActorContext) (domain.MachineRef, error) {
	if err := domain.ValidateSessionID(string(id)); err != nil {
		return "", err
	}
	m.mu.RLock()
	s, ok := m.sessions[id]
	m.mu.RUnlock()
	if ok {
		if !m.authorize(caller, s) {
			return "", domain.ErrSessionNotFound
		}
		s.mu.RLock()
		target := s.obs.Target
		s.mu.RUnlock()
		return target, nil
	}
	if obs, found, err := m.loadDiskSession(id, caller); err != nil {
		return "", err
	} else if found {
		return obs.Target, nil
	}
	return "", domain.ErrSessionNotFound
}
