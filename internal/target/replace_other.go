//go:build !unix && !windows

package target

import (
	"context"
	"errors"
)

func atomicReplace(context.Context, string, string) CommitResult {
	return CommitResult{Err: errors.New("target: atomic replacement is unsupported on this platform")}
}
