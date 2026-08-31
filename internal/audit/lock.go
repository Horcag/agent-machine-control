package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Horcag/agent-machine-control/internal/lease"
)

func (s *Store) withLockContext(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	lockDir := filepath.Join(s.dir, ".audit.lock")
	ownerPath := filepath.Join(lockDir, "owner.json")
	runtimeID, pid, startTime := s.identityProvider.CurrentIdentity()
	now := s.now()
	if err := s.acquireAuditLockContext(ctx, lockDir, ownerPath, runtimeID, pid, startTime, now); err != nil {
		return err
	}
	operationErr := fn()
	cleanupErr := s.releaseLock(ownerPath, lockDir, runtimeID, pid, startTime, now)
	return errors.Join(operationErr, cleanupErr)
}

func (s *Store) acquireAuditLockContext(ctx context.Context, lockDir, ownerPath, runtimeID string, pid int, startTime string, now time.Time) error {
	deadline := auditLockDeadline(ctx, s.lockTimeout)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := os.Mkdir(lockDir, 0700)
		if err == nil {
			return s.recordAuditLockOwner(ownerPath, lockDir, runtimeID, pid, startTime, now)
		}
		if !os.IsExist(err) {
			return fmt.Errorf("%w: failed to create audit lock: %v", ErrAuditUnavailable, err)
		}

		reclaimed, reclaimErr := s.tryReclaimDeadLock(ownerPath, lockDir, runtimeID)
		if reclaimErr != nil {
			return fmt.Errorf("%w: %v", ErrAuditUnavailable, reclaimErr)
		}
		if reclaimed {
			continue
		}
		if err := waitForAuditLockRetry(ctx, deadline); err != nil {
			return err
		}
	}
}

func auditLockDeadline(ctx context.Context, timeout time.Duration) time.Time {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		return ctxDeadline
	}
	return deadline
}

func (s *Store) recordAuditLockOwner(ownerPath, lockDir, runtimeID string, pid int, startTime string, now time.Time) error {
	if err := s.recordLockOwner(ownerPath, runtimeID, pid, startTime, now); err != nil {
		cleanupErr := errors.Join(s.removeOwnedPath(ownerPath), s.removeOwnedPath(lockDir))
		return fmt.Errorf("%w: %v", ErrAuditUnavailable, errors.Join(err, cleanupErr))
	}
	return nil
}

func waitForAuditLockRetry(ctx context.Context, deadline time.Time) error {
	if time.Now().After(deadline) {
		return fmt.Errorf("%w: timeout acquiring audit lock", ErrAuditUnavailable)
	}
	timer := time.NewTimer(5 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *Store) recordLockOwner(ownerPath, runtimeID string, pid int, startTime string, now time.Time) error {
	ownerRec := lease.LockOwnerRecord{
		SchemaVersion: SchemaVersion, RuntimeID: runtimeID, PID: pid,
		ProcessStartTime: startTime, AcquiredAt: now,
	}
	data, err := json.MarshalIndent(ownerRec, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal audit lock owner: %w", err)
	}
	f, err := os.OpenFile(ownerPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("failed to open audit lock owner file: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("failed to write audit lock owner file: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("failed to sync audit lock owner file: %w", err)
	}
	return f.Close()
}

func (s *Store) tryReclaimDeadLock(ownerPath, lockDir, runtimeID string) (bool, error) {
	ownerRec, err := readLockOwner(ownerPath)
	if err != nil {
		return false, nil
	}
	if ownerRec.SchemaVersion != SchemaVersion || ownerRec.RuntimeID != runtimeID || ownerRec.PID <= 0 {
		return false, nil
	}
	alive, checkErr := s.livenessChecker.IsAlive(ownerRec.PID, ownerRec.ProcessStartTime)
	if checkErr != nil || alive {
		return false, nil
	}
	if err := s.removeOwnedPath(ownerPath); err != nil {
		return false, fmt.Errorf("audit: failed to remove stale lock owner: %w", err)
	}
	if err := s.removeOwnedPath(lockDir); err != nil {
		return false, fmt.Errorf("audit: failed to remove stale lock directory: %w", err)
	}
	return true, nil
}

func (s *Store) releaseLock(ownerPath, lockDir, runtimeID string, pid int, startTime string, acquiredAt time.Time) error {
	ownerRec, err := readLockOwner(ownerPath)
	if err != nil {
		return fmt.Errorf("audit: failed to read lock owner during release: %w", err)
	}
	if ownerRec.SchemaVersion != SchemaVersion || ownerRec.RuntimeID != runtimeID || ownerRec.PID != pid ||
		ownerRec.ProcessStartTime != startTime || !ownerRec.AcquiredAt.Equal(acquiredAt) {
		return errors.New("audit: lock ownership changed before release")
	}
	if err := s.removeOwnedPath(ownerPath); err != nil {
		return fmt.Errorf("audit: failed to remove lock owner: %w", err)
	}
	if err := s.removeOwnedPath(lockDir); err != nil {
		return fmt.Errorf("audit: failed to remove lock directory: %w", err)
	}
	return nil
}

func readLockOwner(ownerPath string) (lease.LockOwnerRecord, error) {
	ownerData, err := os.ReadFile(ownerPath)
	if err != nil {
		return lease.LockOwnerRecord{}, err
	}
	var ownerRec lease.LockOwnerRecord
	dec := json.NewDecoder(bytes.NewReader(ownerData))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ownerRec); err != nil {
		return lease.LockOwnerRecord{}, err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return lease.LockOwnerRecord{}, errors.New("audit: lock owner has trailing data")
	}
	return ownerRec, nil
}

func (s *Store) removeOwnedPath(path string) error {
	removeFn := s.removeFn
	if removeFn == nil {
		removeFn = os.Remove
	}
	if err := removeFn(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func lockAuditStoreContext(ctx context.Context, mu *sync.Mutex) error {
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
