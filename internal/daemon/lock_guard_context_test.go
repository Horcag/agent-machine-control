//go:build linux || darwin

package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSingletonObservationWaitCancellationRetainsWriterGuard(t *testing.T) {
	t.Parallel()
	daemonDir := t.TempDir()
	path := filepath.Join(daemonDir, "singleton.guard")
	writer, err := lockSingletonGuard(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	observer, err := LockSingletonObservation(ctx, daemonDir)
	if observer != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended observation = %v, %v; want bounded cancellation", observer, err)
	}
	probe, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	busy, err := tryLockGuardFile(probe)
	if err != nil || !busy {
		t.Fatalf("canceled observer released writer ownership: busy=%v, err=%v", busy, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	observer, err = LockSingletonObservation(context.Background(), daemonDir)
	if err != nil || observer == nil {
		t.Fatalf("observation after writer release = %v, %v", observer, err)
	}
	if err := observer.Close(); err != nil {
		t.Fatal(err)
	}
	busy, err = tryLockGuardFile(probe)
	if err != nil || busy {
		t.Fatalf("closed observer retained guard: busy=%v, err=%v", busy, err)
	}
}

func TestSingletonObservationCanceledBeforeOpenCreatesNothing(t *testing.T) {
	t.Parallel()
	daemonDir := filepath.Join(t.TempDir(), "absent-daemon")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	observer, err := LockSingletonObservation(ctx, daemonDir)
	if observer != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("already canceled observation = %v, %v", observer, err)
	}
	if _, err := os.Lstat(daemonDir); !os.IsNotExist(err) {
		t.Fatalf("canceled observation created state: %v", err)
	}
}

func TestSingletonObservationRejectsUnopenableGuard(t *testing.T) {
	t.Parallel()
	daemonDir := t.TempDir()
	path := filepath.Join(daemonDir, "singleton.guard")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	observer, err := LockSingletonObservation(context.Background(), daemonDir)
	if observer != nil || err == nil {
		t.Fatalf("directory guard = %v, %v; want refusal", observer, err)
	}
}

func TestTrySingletonGuardInvalidDescriptorIsErrorNotContention(t *testing.T) {
	t.Parallel()
	f, err := os.CreateTemp(t.TempDir(), "closed-guard")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	busy, err := tryLockGuardFile(f)
	if busy || err == nil {
		t.Fatalf("invalid descriptor = busy %v, %v; want error without contention", busy, err)
	}
}

func TestSingletonGuardRejectsSymlinkBeforeLocking(t *testing.T) {
	t.Parallel()
	daemonDir := t.TempDir()
	target := filepath.Join(daemonDir, "real-guard")
	if err := os.WriteFile(target, nil, 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(daemonDir, "singleton.guard")
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	guard, err := openSingletonGuard(path, os.O_RDWR, func(*os.File) error {
		t.Error("symlinked guard reached lock acquisition")
		return nil
	})
	if guard != nil || err == nil {
		t.Fatalf("symlinked guard = %v, %v; want identity refusal", guard, err)
	}
}

func TestSingletonGuardRejectsReplacementDuringAcquisition(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "singleton.guard")
	guard, err := openSingletonGuard(path, os.O_CREATE|os.O_RDWR, func(*os.File) error {
		if err := os.Remove(path); err != nil {
			return err
		}
		return os.WriteFile(path, []byte("replacement"), 0600)
	})
	if guard != nil || err == nil {
		t.Fatalf("replaced guard = %v, %v; want identity refusal", guard, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "replacement" {
		t.Fatalf("refusal modified replacement guard: %q, %v", data, err)
	}
}
