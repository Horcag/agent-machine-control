package lease_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/lease"
)

func TestManager_ConcurrentAcquisition_OneWinner(t *testing.T) {
	dir := t.TempDir()
	machineID := "a0b1c2d3-e4f5-6789-abcd-ef0123456789"

	numGoroutines := 10
	var winners atomic.Int32
	var conflicts atomic.Int32
	var errs atomic.Int32

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	aliveMap := make(map[int]bool)
	for i := range numGoroutines {
		aliveMap[1000+i] = true
	}
	checker := &mockLivenessChecker{aliveMap: aliveMap}

	for i := range numGoroutines {
		go func(idx int) {
			defer wg.Done()
			ident := &mockIdentityProvider{
				runtimeID: "test-runtime",
				pid:       1000 + idx,
				startTime: fmt.Sprintf("%d", 1000+idx),
			}
			mgr := lease.NewManager(dir,
				lease.WithClock(func() time.Time { return now }),
				lease.WithIdentityProvider(ident),
				lease.WithLivenessChecker(checker),
			)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			_, err := mgr.Acquire(ctx, machineID, "machine.start", fmt.Sprintf("fp-%d", idx), 30*time.Second)
			switch {
			case err == nil:
				winners.Add(1)
			case errors.Is(err, lease.ErrLeaseConflict):
				conflicts.Add(1)
			default:
				errs.Add(1)
				t.Logf("unexpected error for worker %d: %v", idx, err)
			}
		}(i)
	}

	wg.Wait()

	if winners.Load() != 1 {
		t.Fatalf("expected exactly 1 winner, got %d (conflicts: %d, errors: %d)", winners.Load(), conflicts.Load(), errs.Load())
	}
	if conflicts.Load() != int32(numGoroutines-1) {
		t.Fatalf("expected %d conflicts, got %d", numGoroutines-1, conflicts.Load())
	}
}
