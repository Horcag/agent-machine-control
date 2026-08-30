//go:build !windows

package sessions

import (
	"context"
	"os"
)

func atomicReplace(oldPath, newPath string) (atomicReplaceMethod, error) {
	return atomicReplaceMethodRename, os.Rename(oldPath, newPath)
}

func verifySessionFilePublication(context.Context, string, []byte) error {
	return nil
}
