package approval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	return s.CheckWritableContext(context.Background())
}

// CheckWritableContext verifies approval-store writability within the caller's deadline.
func (s *Store) CheckWritableContext(ctx context.Context) error {
	if err := lockApprovalStoreContext(ctx, &s.mu); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

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

func (s *Store) approvalPath(id string) (string, error) {
	if err := domain.ValidateApprovalID(id); err != nil {
		return "", fmt.Errorf("approval: invalid approval ID: %w", err)
	}
	return filepath.Join(s.dir, fmt.Sprintf("%s.json", id)), nil
}

func (s *Store) issuedApprovalPath(id string) (string, error) {
	if err := domain.ValidateApprovalID(id); err != nil {
		return "", fmt.Errorf("approval: invalid approval ID: %w", err)
	}
	return filepath.Join(s.dir, fmt.Sprintf("%s.issued.json", id)), nil
}

// Issue persists immutable server-side provenance for an approval.
func (s *Store) Issue(a domain.Approval) error {
	return s.IssueContext(context.Background(), a)
}

// IssueContext persists immutable server-side provenance within the caller's deadline.
func (s *Store) IssueContext(ctx context.Context, a domain.Approval) error {
	if err := a.Validate(); err != nil || a.Consumed {
		return fmt.Errorf("approval: invalid issuance record: %w", errors.Join(err, domain.ErrInvalidApprovalRecord))
	}
	if err := lockApprovalStoreContext(ctx, &s.mu); err != nil {
		return err
	}
	defer s.mu.Unlock()
	path, err := s.issuedApprovalPath(string(a.ID))
	if err != nil {
		return err
	}
	return writeApprovalRecordContext(ctx, path, a)
}

// ValidateIssuedContext proves that the supplied approval exactly matches server-issued authority.
func (s *Store) ValidateIssuedContext(ctx context.Context, a domain.Approval) error {
	if err := lockApprovalStoreContext(ctx, &s.mu); err != nil {
		return err
	}
	defer s.mu.Unlock()
	return s.validateIssuedLocked(ctx, a)
}

// LoadIssuedContext loads immutable server-issued authority by canonical ID.
func (s *Store) LoadIssuedContext(ctx context.Context, id string) (*domain.Approval, error) {
	if err := lockApprovalStoreContext(ctx, &s.mu); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	return s.loadIssuedLocked(ctx, id)
}

func (s *Store) loadIssuedLocked(ctx context.Context, id string) (*domain.Approval, error) {
	path, err := s.issuedApprovalPath(id)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	issued, err := LoadFromFile(path)
	if os.IsNotExist(err) || errors.Is(err, os.ErrNotExist) {
		return nil, ErrApprovalNotIssued
	}
	if err != nil {
		return nil, err
	}
	if issued.Consumed {
		return nil, ErrApprovalNotIssued
	}
	return issued, ctx.Err()
}

func (s *Store) validateIssuedLocked(ctx context.Context, a domain.Approval) error {
	issued, err := s.loadIssuedLocked(ctx, string(a.ID))
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(*issued, a) {
		return ErrApprovalNotIssued
	}
	return ctx.Err()
}

// IsConsumed checks whether the approval has already been durably consumed.
func (s *Store) IsConsumed(id string) (bool, error) {
	return s.IsConsumedContext(context.Background(), id)
}

// IsConsumedContext checks consumption within the caller's deadline.
func (s *Store) IsConsumedContext(ctx context.Context, id string) (bool, error) {
	if err := lockApprovalStoreContext(ctx, &s.mu); err != nil {
		return false, err
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}

	path, err := s.approvalPath(id)
	if err != nil {
		return false, err
	}
	fi, err := os.Lstat(path)
	if err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return false, fmt.Errorf("approval: symlink detected for consumed record %s", path)
		}
		if err := validateApprovalFilePrivacy(path, fi); err != nil {
			return false, err
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
	return s.MarkConsumedContext(context.Background(), a, consumedAt)
}

// MarkConsumedContext records one-use consumption within the caller's deadline.
func (s *Store) MarkConsumedContext(ctx context.Context, a domain.Approval, consumedAt time.Time) error {
	if err := lockApprovalStoreContext(ctx, &s.mu); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.validateIssuedLocked(ctx, a); err != nil {
		return err
	}

	consumed := a.Clone()
	consumed.Consumed = true
	consumed.ConsumedAt = &consumedAt

	dto := ConvertToDTO(consumed)
	data, err := json.MarshalIndent(dto, "", "  ")
	if err != nil {
		return fmt.Errorf("approval: failed to marshal consumed record: %w", err)
	}

	path, err := s.approvalPath(string(a.ID))
	if err != nil {
		return err
	}
	f, err := createApprovalFile(path)
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

// ReleaseUnexecutedContext restores an issued approval when admission consumed it
// but durable effect truth proves that no guest mutation occurred.
func (s *Store) ReleaseUnexecutedContext(ctx context.Context, a domain.Approval) error {
	if err := lockApprovalStoreContext(ctx, &s.mu); err != nil {
		return err
	}
	defer s.mu.Unlock()
	path, err := s.approvalPath(string(a.ID))
	if err != nil {
		return err
	}
	consumed, err := LoadFromFile(path)
	if os.IsNotExist(err) || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	restored := consumed.Clone()
	restored.Consumed = false
	restored.ConsumedAt = nil
	if !consumed.Consumed || !reflect.DeepEqual(restored, a) {
		return ErrApprovalNotIssued
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return statedir.SyncDir(s.dir)
}

func writeApprovalRecordContext(ctx context.Context, path string, a domain.Approval) error {
	data, err := json.MarshalIndent(ConvertToDTO(a), "", "  ")
	if err != nil {
		return fmt.Errorf("approval: failed to marshal issuance record: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := createApprovalFile(path)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
		return err
	}
	return statedir.SyncDir(filepath.Dir(path))
}

func lockApprovalStoreContext(ctx context.Context, mu *sync.Mutex) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if mu.TryLock() {
			return nil
		}
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}
