package receipt

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCheckWritableContextCancellationAndLockWait(t *testing.T) {
	store := NewStore(t.TempDir())
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.CheckWritableContext(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled writability error = %v", err)
	}
	store.mu.Lock()
	ctx, timeoutCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	err := store.CheckWritableContext(ctx)
	timeoutCancel()
	store.mu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("locked writability error = %v, want deadline", err)
	}
}
