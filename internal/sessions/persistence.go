package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type atomicReplacement func(context.Context) error

type atomicReplacePreparer func(oldPath, newPath string) (atomicReplacement, error)

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
	filePath, err := sessionStatePath(m.sessionsDir, obs.ID)
	if err != nil {
		return fmt.Errorf("sessions: failed to resolve session %s state path: %w", obs.ID, err)
	}
	if err := sessionStateExistsSafely(m.sessionsDir, obs.ID); err != nil {
		return err
	}
	if err := replaceSessionFile(filePath, data); err != nil {
		return fmt.Errorf("sessions: failed to persist session %s: %w", obs.ID, err)
	}
	return nil
}

func replaceSessionFile(filePath string, data []byte) error {
	return replaceSessionFileContext(context.Background(), filePath, data)
}

func replaceSessionFileContext(ctx context.Context, filePath string, data []byte) error {
	return replaceSessionFileContextWithPreparer(ctx, filePath, data, prepareAtomicReplace)
}

func replaceSessionFileContextWithPreparer(
	ctx context.Context,
	filePath string,
	data []byte,
	prepare atomicReplacePreparer,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	replace, err := prepare(tmpPath, filePath)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := replace(ctx); err != nil {
		return err
	}
	if err := syncSessionDirectory(dir); err != nil {
		return err
	}
	return nil
}
