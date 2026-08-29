package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

var (
	// ErrDaemonRunning indicates an active daemon instance already holds the singleton lock.
	ErrDaemonRunning = errors.New("daemon: another daemon instance is already active")
)

// SingletonLock manages exclusive daemon process execution.
type SingletonLock struct {
	lockDir          string
	ownerPath        string
	runtimeID        string
	pid              int
	startTime        string
	acquiredAt       time.Time
	livenessChecker  lease.LivenessChecker
	identityProvider lease.IdentityProvider
}

// AcquireSingletonLock attempts to acquire the daemon singleton lock, reclaiming only on confirmed death.
func AcquireSingletonLock(
	daemonDir string,
	ident lease.IdentityProvider,
	checker lease.LivenessChecker,
	now time.Time,
) (*SingletonLock, error) {
	lockDir := filepath.Join(daemonDir, "singleton.lock")
	ownerPath := filepath.Join(lockDir, "owner.json")

	runtimeID, pid, startTime := ident.CurrentIdentity()

	for range 3 {
		err := os.Mkdir(lockDir, 0700)
		if err == nil {
			if err := writeOwnerRecord(lockDir, ownerPath, daemonDir, runtimeID, pid, startTime, now); err != nil {
				return nil, err
			}
			return &SingletonLock{
				lockDir:          lockDir,
				ownerPath:        ownerPath,
				runtimeID:        runtimeID,
				pid:              pid,
				startTime:        startTime,
				acquiredAt:       now,
				livenessChecker:  checker,
				identityProvider: ident,
			}, nil
		}

		if !os.IsExist(err) {
			return nil, fmt.Errorf("daemon: failed to create lock dir: %w", err)
		}

		if err := tryReclaimLock(ownerPath, lockDir, daemonDir, runtimeID, checker); err != nil {
			return nil, err
		}
	}

	return nil, ErrDaemonRunning
}

func writeOwnerRecord(lockDir, ownerPath, daemonDir, runtimeID string, pid int, startTime string, now time.Time) (retErr error) {
	ownerRec := lease.LockOwnerRecord{
		SchemaVersion:    SchemaVersion,
		RuntimeID:        runtimeID,
		PID:              pid,
		ProcessStartTime: startTime,
		AcquiredAt:       now,
	}
	data, err := json.MarshalIndent(ownerRec, "", "  ")
	if err != nil {
		_ = os.Remove(lockDir)
		return err
	}

	defer func() {
		if retErr != nil {
			_ = os.Remove(ownerPath)
			_ = os.Remove(lockDir)
		}
	}()

	f, err := os.OpenFile(ownerPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := statedir.SyncDir(lockDir); err != nil {
		return err
	}
	return statedir.SyncDir(daemonDir)
}

func tryReclaimLock(ownerPath, lockDir, daemonDir, runtimeID string, checker lease.LivenessChecker) error {
	ownerRec, readErr := readOwnerRecord(ownerPath)
	if readErr != nil {
		return ErrDaemonRunning
	}

	if ownerRec.RuntimeID != runtimeID {
		return ErrDaemonRunning
	}

	alive, checkErr := checker.IsAlive(ownerRec.PID, ownerRec.ProcessStartTime)
	if checkErr != nil || alive {
		return ErrDaemonRunning
	}

	if err := os.Remove(ownerPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("daemon: failed to remove stale singleton owner: %w", err)
	}
	if err := os.Remove(lockDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("daemon: failed to remove stale singleton lock directory: %w", err)
	}
	if err := statedir.SyncDir(daemonDir); err != nil {
		return fmt.Errorf("daemon: failed to sync daemon dir after stale lock reclaim: %w", err)
	}
	return nil
}

func readOwnerRecord(ownerPath string) (*lease.LockOwnerRecord, error) {
	fi, err := os.Lstat(ownerPath)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("daemon: singleton owner is a symlink")
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("daemon: singleton owner is not a regular file")
	}
	if fi.Size() > 4096 {
		return nil, fmt.Errorf("daemon: singleton owner file too large")
	}

	data, err := os.ReadFile(ownerPath)
	if err != nil {
		return nil, err
	}

	var ownerRec lease.LockOwnerRecord
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ownerRec); err != nil {
		return nil, err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("daemon: trailing data in owner file")
	}

	if ownerRec.SchemaVersion != SchemaVersion || ownerRec.PID <= 0 || ownerRec.RuntimeID == "" {
		return nil, fmt.Errorf("daemon: invalid owner record")
	}

	return &ownerRec, nil
}

// Release releases the singleton lock if still owned by this process identity.
func (l *SingletonLock) Release() error {
	ownerRec, err := readOwnerRecord(l.ownerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("daemon: failed to read singleton owner during release: %w", err)
	}

	if ownerRec.RuntimeID == l.runtimeID && ownerRec.PID == l.pid && ownerRec.ProcessStartTime == l.startTime {
		if err := os.Remove(l.ownerPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("daemon: failed to remove singleton owner: %w", err)
		}
		if err := os.Remove(l.lockDir); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("daemon: failed to remove singleton lock directory: %w", err)
		}
		return statedir.SyncDir(filepath.Dir(l.lockDir))
	}
	return nil
}
