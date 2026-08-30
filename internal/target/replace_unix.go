//go:build unix

package target

import (
	"context"
	"os"
)

func atomicReplace(ctx context.Context, oldPath, newPath string) CommitResult {
	if err := ctx.Err(); err != nil {
		return CommitResult{Err: err}
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		return CommitResult{Err: err}
	}
	return CommitResult{Committed: true}
}
