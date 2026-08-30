//go:build !windows

package sessions

import (
	"context"
	"os"

	"github.com/Horcag/agent-machine-control/internal/statedir"
)

func prepareAtomicReplace(oldPath, newPath string) (atomicReplacement, error) {
	return func(ctx context.Context) publicationResult {
		if err := ctx.Err(); err != nil {
			return publicationResult{Err: err}
		}
		if err := os.Rename(oldPath, newPath); err != nil {
			return publicationResult{Err: err}
		}
		return publicationResult{Committed: true}
	}, nil
}

func syncSessionDirectory(dir string) error {
	return statedir.SyncDir(dir)
}
