package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

// Wait blocks for quiet settle time or regex pattern match against output.
func (m *Manager) Wait(ctx context.Context, id domain.SessionID, caller domain.ActorContext, settle time.Duration, regexStr string, afterSeq uint64, timeout time.Duration) ([]domain.SessionChunk, uint64, uint64, bool, *domain.SessionObservation, error) {
	if !caller.HasScope(domain.ScopeSessionRead) {
		return nil, 0, 0, false, nil, domain.ErrSessionAccessDenied
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

func (m *Manager) loadDiskSession(id domain.SessionID, caller domain.ActorContext) (*domain.SessionObservation, bool) {
	if m.sessionsDir == "" {
		return nil, false
	}
	filePath := filepath.Join(m.sessionsDir, fmt.Sprintf("%s.json", id))
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, false
	}
	var obs domain.SessionObservation
	if err := json.Unmarshal(data, &obs); err != nil {
		return nil, false
	}
	if string(caller.EffectiveActor) != string(obs.OwnerActor) && !caller.HasScope(domain.ScopeSessionAdmin) {
		return nil, false
	}
	return &obs, true
}

func (m *Manager) loadDiskSessions(target domain.MachineRef, caller domain.ActorContext, seen map[domain.SessionID]bool) []domain.SessionObservation {
	if m.sessionsDir == "" {
		return nil
	}
	entries, err := os.ReadDir(m.sessionsDir)
	if err != nil {
		return nil
	}

	var results []domain.SessionObservation
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || !strings.HasPrefix(entry.Name(), "sess-") {
			continue
		}
		id := domain.SessionID(strings.TrimSuffix(entry.Name(), ".json"))
		if seen[id] {
			continue
		}
		obs, ok := m.loadDiskSession(id, caller)
		if !ok {
			continue
		}
		if target != "" && obs.Target != target {
			continue
		}
		seen[id] = true
		results = append(results, *obs)
	}
	return results
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
		if target != "" && s.obs.Target != target {
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

	diskSessions := m.loadDiskSessions(target, caller, seen)
	result = append(result, diskSessions...)

	return result, nil
}

// Get returns the current observation of a session.
func (m *Manager) Get(_ context.Context, id domain.SessionID, caller domain.ActorContext) (*domain.SessionObservation, error) {
	if !caller.HasScope(domain.ScopeSessionRead) {
		return nil, domain.ErrSessionAccessDenied
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

	if obs, found := m.loadDiskSession(id, caller); found {
		return obs, nil
	}

	return nil, domain.ErrSessionNotFound
}

// MutationTarget resolves only the target needed to authorize a session mutation.
// It enforces ownership without requiring or disclosing session:read metadata.
func (m *Manager) MutationTarget(id domain.SessionID, caller domain.ActorContext) (domain.MachineRef, error) {
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
	if obs, found := m.loadDiskSession(id, caller); found {
		return obs.Target, nil
	}
	return "", domain.ErrSessionNotFound
}
