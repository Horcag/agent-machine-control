//go:build windows

package target

import (
	"context"
	"fmt"

	"golang.org/x/sys/windows"
)

func atomicReplace(ctx context.Context, oldPath, newPath string) CommitResult {
	if err := ctx.Err(); err != nil {
		return CommitResult{Err: err}
	}
	oldPath16, err := windows.UTF16PtrFromString(oldPath)
	if err != nil {
		return CommitResult{Err: fmt.Errorf("target: replacement source path: %w", err)}
	}
	newPath16, err := windows.UTF16PtrFromString(newPath)
	if err != nil {
		return CommitResult{Err: fmt.Errorf("target: replacement destination path: %w", err)}
	}
	if err := windows.MoveFileEx(oldPath16, newPath16, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return CommitResult{Err: fmt.Errorf("target: atomic replacement: %w", err)}
	}
	return CommitResult{Committed: true}
}
