package lease

import (
	"bytes"
	"context"
	"encoding/json"
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
	lockDir := m.lockPath(machineID)
	ownerPath := filepath.Join(lockDir, "owner.json")
	runtimeID, pid, startTime := m.identityProvider.CurrentIdentity()
	now := m.now()
	if err := m.acquireTransitionLock(ctx, lockDir, ownerPath, runtimeID, pid, startTime, now); err != nil {
		return err
	}
	defer m.releaseLock(ownerPath, lockDir, runtimeID, pid, startTime, now)
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
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
		if m.tryReclaimDeadLock(ownerPath, lockDir, runtimeID) {
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
	if err := os.Mkdir(lockDir, 0700); err != nil {
		if os.IsExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to create lock directory %q: %w", lockDir, err)
	}
	if err := ctx.Err(); err != nil {
		_ = os.Remove(lockDir)
		return false, err
	}
	if err := m.recordLockOwner(ownerPath, runtimeID, pid, startTime, now); err != nil {
		_ = os.Remove(ownerPath)
		_ = os.Remove(lockDir)
		return false, err
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

func (m *Manager) tryReclaimDeadLock(ownerPath, lockDir, runtimeID string) bool {
	ownerData, err := os.ReadFile(ownerPath)
	if err != nil {
		return false
	}
	var ownerRec LockOwnerRecord
	dec := json.NewDecoder(bytes.NewReader(ownerData))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ownerRec); err != nil {
		return false
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return false
	}
	if ownerRec.SchemaVersion != SchemaVersion || ownerRec.RuntimeID != runtimeID || ownerRec.PID <= 0 {
		return false
	}
	alive, checkErr := m.livenessChecker.IsAlive(ownerRec.PID, ownerRec.ProcessStartTime)
	if checkErr != nil || alive {
		return false
	}
	_ = os.Remove(ownerPath)
	_ = os.Remove(lockDir)
	return true
}

func (m *Manager) releaseLock(ownerPath, lockDir, runtimeID string, pid int, startTime string, acquiredAt time.Time) {
	ownerData, err := os.ReadFile(ownerPath)
	if err != nil {
		return
	}
	var ownerRec LockOwnerRecord
	dec := json.NewDecoder(bytes.NewReader(ownerData))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ownerRec); err != nil {
		return
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return
	}
	if ownerRec.SchemaVersion == SchemaVersion &&
		ownerRec.RuntimeID == runtimeID &&
		ownerRec.PID == pid &&
		ownerRec.ProcessStartTime == startTime &&
		ownerRec.AcquiredAt.Equal(acquiredAt) {
		_ = os.Remove(ownerPath)
		_ = os.Remove(lockDir)
	}
}
