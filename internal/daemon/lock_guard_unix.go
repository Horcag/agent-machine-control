//go:build linux || darwin

package daemon

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockGuardFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX)
}
