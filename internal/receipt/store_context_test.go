package receipt_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func TestStoreEnsureContextHonorsDeadlineWithoutLateWrite(t *testing.T) {
	dir := t.TempDir()
	entered := make(chan struct{})
	store := receipt.NewStore(dir, receipt.WithEnsureHook(func(ctx context.Context, _ domain.Receipt) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := store.EnsureContext(ctx, ensureTestReceipt()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ensure context error = %v, want deadline", err)
	}
	select {
	case <-entered:
	default:
		t.Fatal("context-aware ensure hook was not entered")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("deadline ensure left receipt state: entries=%v err=%v", entries, err)
	}
}
