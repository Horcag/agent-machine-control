//go:build !windows

package sessions

import (
	"context"
	"os"

	"github.com/Horcag/agent-machine-control/internal/statedir"
)

func prepareAtomicReplace(oldPath, newPath string) (atomicReplacement, error) {
	return func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return os.Rename(oldPath, newPath)
	}, nil
}

func syncSessionDirectory(dir string) error {
	return statedir.SyncDir(dir)
}
