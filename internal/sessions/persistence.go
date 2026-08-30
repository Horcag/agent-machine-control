package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type publicationResult struct {
	Committed bool
	Durable   bool
	Err       error
}

func (r publicationResult) failedBeforeCommit() bool {
	return r.Err != nil && !r.Committed
}

type atomicReplacement func(context.Context) publicationResult

type atomicReplacePreparer func(oldPath, newPath string) (atomicReplacement, error)

func (m *Manager) persistSession(s *Session) publicationResult {
	if m.sessionsDir == "" {
		return publicationResult{Committed: true, Durable: true}
	}

	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	s.mu.RLock()
	obs := s.obs
	s.mu.RUnlock()

	data, err := json.MarshalIndent(obs, "", "  ")
	if err != nil {
		return publicationResult{Err: fmt.Errorf("sessions: failed to marshal session %s: %w", obs.ID, err)}
	}
	filePath, err := sessionStatePath(m.sessionsDir, obs.ID)
	if err != nil {
		return publicationResult{Err: fmt.Errorf("sessions: failed to resolve session %s state path: %w", obs.ID, err)}
	}
	if err := sessionStateExistsSafely(m.sessionsDir, obs.ID); err != nil {
		return publicationResult{Err: err}
	}
	result := replaceSessionFileWithSync(filePath, data, m.syncSessionDir)
	if result.Err != nil {
		result.Err = fmt.Errorf("sessions: failed to persist session %s: %w", obs.ID, result.Err)
	}
	return result
}

func replaceSessionFile(filePath string, data []byte) publicationResult {
	return replaceSessionFileContext(context.Background(), filePath, data)
}

func replaceSessionFileWithSync(filePath string, data []byte, syncDir func(string) error) publicationResult {
	return replaceSessionFileContextWithPreparer(context.Background(), filePath, data, prepareAtomicReplace, syncDir)
}

func replaceSessionFileContext(ctx context.Context, filePath string, data []byte) publicationResult {
	return replaceSessionFileContextWithPreparer(ctx, filePath, data, prepareAtomicReplace, syncSessionDirectory)
}

func replaceSessionFileContextWithPreparer(
	ctx context.Context,
	filePath string,
	data []byte,
	prepare atomicReplacePreparer,
	syncDir func(string) error,
) publicationResult {
	if err := ctx.Err(); err != nil {
		return publicationResult{Err: err}
	}
	dir := filepath.Dir(filePath)
	tmp, err := os.CreateTemp(dir, ".session-*.tmp")
	if err != nil {
		return publicationResult{Err: err}
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()

	if err := tmp.Chmod(0600); err != nil {
		return publicationResult{Err: err}
	}
	if _, err := tmp.Write(data); err != nil {
		return publicationResult{Err: err}
	}
	if err := ctx.Err(); err != nil {
		return publicationResult{Err: err}
	}
	if err := tmp.Sync(); err != nil {
		return publicationResult{Err: err}
	}
	if err := tmp.Close(); err != nil {
		return publicationResult{Err: err}
	}
	replace, err := prepare(tmpPath, filePath)
	if err != nil {
		return publicationResult{Err: err}
	}
	if err := ctx.Err(); err != nil {
		return publicationResult{Err: err}
	}
	result := replace(ctx)
	if result.Err != nil {
		return result
	}
	if !result.Committed {
		return publicationResult{Err: errors.New("sessions: atomic replacement returned without a commit")}
	}
	if syncDir == nil {
		syncDir = syncSessionDirectory
	}
	if err := syncDir(dir); err != nil {
		return publicationResult{
			Committed: true,
			Err:       fmt.Errorf("sessions: canonical state committed but directory sync failed: %w", err),
		}
	}
	return publicationResult{Committed: true, Durable: true}
}
