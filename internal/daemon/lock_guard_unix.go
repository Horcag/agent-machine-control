//go:build linux || darwin

package daemon

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lockGuardFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX)
}

func tryLockGuardFile(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return true, nil
	}
	return false, err
}
