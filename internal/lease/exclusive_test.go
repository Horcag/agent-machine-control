package lease_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/lease"
)

func TestExclusiveLockRejectsCanceledAndIncompleteCalls(t *testing.T) {
	manager := lease.NewManager(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	if err := manager.WithExclusiveLock(ctx, "enrollment", func() error { called = true; return nil }); !errors.Is(err, context.Canceled) || called {
		t.Fatalf("canceled critical section: called=%v, err=%v", called, err)
	}
	if err := manager.WithExclusiveLock(context.Background(), "enrollment", nil); err == nil {
		t.Fatal("nil callback accepted")
	}
	var absent *lease.Manager
	if err := absent.WithExclusiveLock(context.Background(), "enrollment", func() error { return nil }); err == nil {
		t.Fatal("nil manager accepted")
	}
}

func TestExclusiveLockRejectsConcurrentEntryAndReleasesAfterFailure(t *testing.T) {
	directory := t.TempDir()
	first, second := lease.NewManager(directory), lease.NewManager(directory)
	failure := errors.New("synthetic operation failure")
	err := first.WithExclusiveLock(context.Background(), "enrollment", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		entered := false
		err := second.WithExclusiveLock(ctx, "enrollment", func() error { entered = true; return nil })
		if entered || (!errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, lease.ErrLeaseConflict)) {
			t.Fatalf("second manager crossed exclusion: entered=%v, err=%v", entered, err)
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("operation error lost: %v", err)
	}
	entered := false
	if err := second.WithExclusiveLock(context.Background(), "enrollment", func() error { entered = true; return nil }); err != nil || !entered {
		t.Fatalf("failed operation leaked lock: entered=%v, err=%v", entered, err)
	}
}
