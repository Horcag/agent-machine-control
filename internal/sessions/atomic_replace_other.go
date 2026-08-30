//go:build !windows

package sessions

import "os"

func atomicReplace(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}
