package approval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

// Store manages tracking consumed approvals on disk to prevent replay attacks.
type Store struct {
	dir string
	mu  sync.Mutex
}

// NewStore creates an Approval Store for the given approvals directory.
func NewStore(dir string) *Store {
	return &Store{dir: dir}
}

// CheckWritable verifies that the approval store can durably create new records.
func (s *Store) CheckWritable() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	probe := filepath.Join(s.dir, fmt.Sprintf(".write-test-%d", time.Now().UnixNano()))
	f, err := os.OpenFile(probe, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("approval: store is unwritable: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(probe)
		return fmt.Errorf("approval: failed to close writability probe: %w", err)
	}
	if err := os.Remove(probe); err != nil {
		return fmt.Errorf("approval: failed to remove writability probe: %w", err)
	}
	return statedir.SyncDir(s.dir)
}

func (s *Store) approvalPath(id string) string {
	return filepath.Join(s.dir, fmt.Sprintf("%s.json", id))
}

// IsConsumed checks whether the approval has already been durably consumed.
func (s *Store) IsConsumed(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.approvalPath(id)
	fi, err := os.Lstat(path)
	if err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return false, fmt.Errorf("approval: symlink detected for consumed record %s", path)
		}
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// MarkConsumed atomically records that an approval was consumed at consumedAt.
// It uses atomic creation with O_CREATE|O_EXCL to prevent race conditions across multiple processes.
func (s *Store) MarkConsumed(a domain.Approval, consumedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	consumed := a.Clone()
	consumed.Consumed = true
	consumed.ConsumedAt = &consumedAt

	dto := ConvertToDTO(consumed)
	data, err := json.MarshalIndent(dto, "", "  ")
	if err != nil {
		return fmt.Errorf("approval: failed to marshal consumed record: %w", err)
	}

	path := s.approvalPath(string(a.ID))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if os.IsExist(err) {
			return domain.ErrApprovalConsumed
		}
		return fmt.Errorf("approval: failed to create consumed record: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("approval: failed to write consumed record: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("approval: failed to sync consumed record: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("approval: failed to close consumed record: %w", err)
	}

	if err := statedir.SyncDir(s.dir); err != nil {
		return fmt.Errorf("approval: failed to sync approvals directory: %w", err)
	}

	return nil
}
