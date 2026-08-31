package receipt_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/statedir"
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

func TestStoreEnsureContextCancellationAfterRenameDoesNotReclassifyCommit(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	store := receipt.NewStore(dir, receipt.WithSyncDir(func(dir string) error {
		cancel()
		return statedir.SyncDir(dir)
	}))
	want := ensureTestReceipt()

	if err := store.EnsureContext(ctx, want); err != nil {
		t.Fatalf("committed receipt was reclassified by cancellation: %v", err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("context error = %v, want canceled at post-rename sync", ctx.Err())
	}
	if err := receipt.NewStore(dir).Ensure(want); err != nil {
		t.Fatalf("exact retry after committed cancellation failed: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != string(want.ReceiptID)+".json" {
		t.Fatalf("receipt entries = %v err %v, want one canonical receipt", entries, err)
	}
}
