package sessions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Horcag/agent-machine-control/internal/statedir"
)

// CheckWritable verifies that the journal can create and remove durable markers.
func (j *MutationJournal) CheckWritable() error {
	return j.CheckWritableContext(context.Background())
}

// CheckWritableContext verifies journal writability within the caller's deadline.
func (j *MutationJournal) CheckWritableContext(ctx context.Context) error {
	if j == nil || j.dir == "" {
		return errors.New("sessions: mutation journal is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if j.contextHook != nil {
		if err := j.contextHook(ctx, "check_writable"); err != nil {
			return err
		}
	}
	if err := j.ensureDir(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	probe := filepath.Join(j.dir, fmt.Sprintf(".write-test-%d", time.Now().UnixNano()))
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
	return statedir.SyncDir(j.dir)
}
