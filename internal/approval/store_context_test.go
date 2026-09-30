package approval

import (
	"context"
	"errors"
	"os"
	"testing"
	"testing/synctest"
)

func TestStoreCanceledLockWaitDoesNotWriteOrPreventLaterAccess(t *testing.T) {
	store := NewStore(t.TempDir())
	synctest.Test(t, func(t *testing.T) {
		store.mu.Lock()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := make(chan error, 1)
		go func() {
			result <- store.CheckWritableContext(ctx)
		}()

		synctest.Wait()
		select {
		case err := <-result:
			store.mu.Unlock()
			t.Fatalf("writability check bypassed held lock: %v", err)
		default:
		}
		cancel()
		err := <-result
		store.mu.Unlock()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled lock wait = %v, want context.Canceled", err)
		}
	})

	entries, err := os.ReadDir(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("canceled writability check wrote store entries: %v", entries)
	}
	if err := store.CheckWritable(); err != nil {
		t.Fatalf("canceled waiter prevented later store access: %v", err)
	}
}
