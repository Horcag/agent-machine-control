package sessions

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Horcag/agent-machine-control/internal/statedir"
)

func (m *Manager) persistSession(s *Session) error {
	if m.sessionsDir == "" {
		return nil
	}

	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	s.mu.RLock()
	obs := s.obs
	s.mu.RUnlock()

	data, err := json.MarshalIndent(obs, "", "  ")
	if err != nil {
		return fmt.Errorf("sessions: failed to marshal session %s: %w", obs.ID, err)
	}
	filePath := filepath.Join(m.sessionsDir, fmt.Sprintf("%s.json", obs.ID))
	if err := replaceSessionFile(filePath, data); err != nil {
		return fmt.Errorf("sessions: failed to persist session %s: %w", obs.ID, err)
	}
	return nil
}

func replaceSessionFile(filePath string, data []byte) error {
	dir := filepath.Dir(filePath)
	tmp, err := os.CreateTemp(dir, ".session-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()

	if err := tmp.Chmod(0600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := atomicReplace(tmpPath, filePath); err != nil {
		return err
	}
	if err := statedir.SyncDir(dir); err != nil {
		return err
	}
	return nil
}
