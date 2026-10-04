package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The guard is persistent: unlinking it would let processes lock different file objects.
func lockSingletonGuard(path string) (*os.File, error) {
	return openSingletonGuard(path, os.O_CREATE|os.O_RDWR, lockGuardFile)
}

// LockSingletonObservation waits for an existing singleton transition to finish.
// It never creates state and bounds observation by the caller deadline or one second.
func LockSingletonObservation(ctx context.Context, daemonDir string) (*os.File, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := filepath.Join(daemonDir, "singleton.guard")
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return lockOpenedSingletonGuard(path, f, func(f *os.File) error {
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			busy, err := tryLockGuardFile(f)
			if err != nil || !busy {
				return err
			}
			timer := time.NewTimer(10 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	})
}

func openSingletonGuard(path string, flags int, lock func(*os.File) error) (*os.File, error) {
	f, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		return nil, err
	}
	return lockOpenedSingletonGuard(path, f, lock)
}

func lockOpenedSingletonGuard(path string, f *os.File, lock func(*os.File) error) (*os.File, error) {
	info, err := f.Stat()
	if err == nil {
		var pathInfo os.FileInfo
		pathInfo, err = os.Lstat(path)
		if err == nil && (!info.Mode().IsRegular() || !pathInfo.Mode().IsRegular() || !os.SameFile(info, pathInfo)) {
			err = fmt.Errorf("singleton guard is not a stable regular file")
		}
	}
	if err == nil {
		err = lock(f)
	}
	if err == nil {
		var lockedPathInfo os.FileInfo
		lockedPathInfo, err = os.Lstat(path)
		if err == nil && (!lockedPathInfo.Mode().IsRegular() || !os.SameFile(info, lockedPathInfo)) {
			err = fmt.Errorf("singleton guard changed while acquiring lock")
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
