//go:build windows

package daemon

import (
	"os"

	"golang.org/x/sys/windows"
)

func lockGuardFile(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &overlapped)
}
