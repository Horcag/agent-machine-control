//go:build windows

package sessions

import "golang.org/x/sys/windows"

func atomicReplace(oldPath, newPath string) error {
	return windows.Rename(oldPath, newPath)
}
