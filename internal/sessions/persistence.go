package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Horcag/agent-machine-control/internal/statedir"
)

type atomicReplaceMethod string

const (
	atomicReplaceMethodRename           atomicReplaceMethod = "rename"
	atomicReplaceMethodFileRenameInfoEx atomicReplaceMethod = "FileRenameInfoEx"
	atomicReplaceMethodMoveFileEx       atomicReplaceMethod = "MoveFileEx"
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
	method, err := atomicReplace(tmpPath, filePath)
	if err != nil {
		return err
	}
	if err := statedir.SyncDir(dir); err != nil {
		return err
	}
	if err := verifySessionFilePublication(ctx, filePath, data); err != nil {
		return fmt.Errorf("sessions: canonical publication after %s failed: %w", method, err)
	}
	return nil
}
