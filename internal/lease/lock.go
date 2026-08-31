package lease

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

func (m *Manager) withLock(ctx context.Context, machineID string, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	lockDir, err := m.lockPath(machineID)
	if err != nil {
		return err
	}
	ownerPath := filepath.Join(lockDir, "owner.json")
	runtimeID, pid, startTime := m.identityProvider.CurrentIdentity()
	now := m.now()
	if err := m.acquireTransitionLock(ctx, lockDir, ownerPath, runtimeID, pid, startTime, now); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, m.releaseLock(ownerPath, lockDir, runtimeID, pid, startTime, now))
	}
	operationErr := fn()
	cleanupErr := m.releaseLock(ownerPath, lockDir, runtimeID, pid, startTime, now)
	return errors.Join(operationErr, cleanupErr)
}

func (m *Manager) acquireTransitionLock(ctx context.Context, lockDir, ownerPath, runtimeID string, pid int, startTime string, now time.Time) error {
	deadline := time.Now().Add(5 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		created, err := m.tryCreateTransitionLock(ctx, lockDir, ownerPath, runtimeID, pid, startTime, now)
		if err != nil {
			return err
		}
		if created {
			return nil
		}
		reclaimed, reclaimErr := m.tryReclaimDeadLock(ownerPath, lockDir, runtimeID)
		if reclaimErr != nil {
			return reclaimErr
		}
		if reclaimed {
			continue
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return fmt.Errorf("timed out waiting for lease transition lock: %w", ErrLeaseConflict)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (m *Manager) tryCreateTransitionLock(ctx context.Context, lockDir, ownerPath, runtimeID string, pid int, startTime string, now time.Time) (bool, error) {
	created, err := createTransitionLockDir(lockDir)
	if err != nil {
		return false, fmt.Errorf("failed to create lock directory %q: %w", lockDir, err)
	}
	if !created {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, errors.Join(err, m.removeOwnedPath(lockDir))
	}
	if err := m.recordLockOwner(ownerPath, runtimeID, pid, startTime, now); err != nil {
		cleanupErr := errors.Join(m.removeOwnedPath(ownerPath), m.removeOwnedPath(lockDir))
		return false, errors.Join(err, cleanupErr)
	}
	return true, nil
}

func (m *Manager) recordLockOwner(ownerPath, runtimeID string, pid int, startTime string, now time.Time) error {
	ownerRec := LockOwnerRecord{
		SchemaVersion:    SchemaVersion,
		RuntimeID:        runtimeID,
		PID:              pid,
		ProcessStartTime: startTime,
		AcquiredAt:       now,
	}
	ownerData, err := json.MarshalIndent(ownerRec, "", "  ")
	if err != nil {
		return fmt.Errorf("lease: failed to marshal lock owner: %w", err)
	}

	f, err := os.OpenFile(ownerPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("lease: failed to create lock owner file: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(ownerData); err != nil {
		return fmt.Errorf("lease: failed to write lock owner file: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("lease: failed to sync lock owner file: %w", err)
	}
	return f.Close()
}

func (m *Manager) tryReclaimDeadLock(ownerPath, lockDir, runtimeID string) (bool, error) {
	ownerData, err := readTransitionLockOwner(ownerPath)
	if err != nil {
		return false, nil
	}
	var ownerRec LockOwnerRecord
	dec := json.NewDecoder(bytes.NewReader(ownerData))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ownerRec); err != nil {
		return false, nil
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return false, nil
	}
	if ownerRec.SchemaVersion != SchemaVersion || ownerRec.RuntimeID != runtimeID || ownerRec.PID <= 0 {
		return false, nil
	}
	alive, checkErr := m.livenessChecker.IsAlive(ownerRec.PID, ownerRec.ProcessStartTime)
	if checkErr != nil || alive {
		return false, nil
	}
	if err := m.removeOwnedPath(ownerPath); err != nil {
		return false, fmt.Errorf("lease: failed to remove stale lock owner: %w", err)
	}
	if err := m.removeOwnedPath(lockDir); err != nil {
		return false, fmt.Errorf("lease: failed to remove stale lock directory: %w", err)
	}
	return true, nil
}

func (m *Manager) releaseLock(ownerPath, lockDir, runtimeID string, pid int, startTime string, acquiredAt time.Time) error {
	ownerData, err := readTransitionLockOwner(ownerPath)
	if err != nil {
		return fmt.Errorf("lease: failed to read lock owner during release: %w", err)
	}
	var ownerRec LockOwnerRecord
	dec := json.NewDecoder(bytes.NewReader(ownerData))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ownerRec); err != nil {
		return fmt.Errorf("lease: failed to decode lock owner during release: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return errors.New("lease: lock owner has trailing data during release")
	}
	if ownerRec.SchemaVersion == SchemaVersion &&
		ownerRec.RuntimeID == runtimeID &&
		ownerRec.PID == pid &&
		ownerRec.ProcessStartTime == startTime &&
		ownerRec.AcquiredAt.Equal(acquiredAt) {
		if err := m.removeOwnedPath(ownerPath); err != nil {
			return fmt.Errorf("lease: failed to remove lock owner: %w", err)
		}
		if err := m.removeOwnedPath(lockDir); err != nil {
			return fmt.Errorf("lease: failed to remove lock directory: %w", err)
		}
		return nil
	}
	return errors.New("lease: lock ownership changed before release")
}

func (m *Manager) removeOwnedPath(path string) error {
	removeFn := m.removeFn
	if removeFn == nil {
		removeFn = os.Remove
	}
	if err := removePathBounded(removeFn, path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
