//go:build linux || darwin

package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSingletonObservationInitialGuardAbsenceDoesNotCreateState(t *testing.T) {
	t.Parallel()
	daemonDir := filepath.Join(t.TempDir(), "absent-daemon")
	guard, err := LockSingletonObservation(context.Background(), daemonDir)
	if err != nil || guard != nil {
		t.Fatalf("initial guard absence = %v, %v; want no guard and no error", guard, err)
	}
	if _, err := os.Lstat(daemonDir); !os.IsNotExist(err) {
		t.Fatalf("observation created absent state: %v", err)
	}
}

func TestSingletonObservationGuardDisappearanceAfterOpenFailsClosed(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"before lock", "during lock"} {
		t.Run(phase, func(t *testing.T) {
			assertSingletonGuardDisappearance(t, phase)
		})
	}
}

func assertSingletonGuardDisappearance(t *testing.T, phase string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "singleton.guard")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if phase == "before lock" {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	guard, err := lockOpenedSingletonGuard(path, f, func(*os.File) error {
		if phase == "before lock" {
			t.Fatal("disappeared guard reached lock acquisition")
		}
		return os.Remove(path)
	})
	if guard != nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disappeared opened guard = %v, %v; want propagated absence error", guard, err)
	}
	if _, err := f.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("rejected observation leaked opened file: %v", err)
	}
}
