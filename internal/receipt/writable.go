package receipt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Horcag/agent-machine-control/internal/statedir"
)

// CheckWritable verifies that a new durable receipt can be created in the store.
func (s *Store) CheckWritable() error {
	return s.CheckWritableContext(context.Background())
}

// CheckWritableContext verifies writability within the caller's deadline.
func (s *Store) CheckWritableContext(ctx context.Context) error {
	if err := lockReceiptStoreContext(ctx, &s.mu); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	probe := filepath.Join(s.dir, fmt.Sprintf(".write-test-%d", time.Now().UnixNano()))
	f, err := os.OpenFile(probe, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(probe)
		return err
	}
	if err := os.Remove(probe); err != nil {
		return err
	}
	return statedir.SyncDir(s.dir)
}
