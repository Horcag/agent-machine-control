//go:build windows

package lease

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func createTransitionLockDir(path string) (bool, error) {
	deadline := time.Now().Add(250 * time.Millisecond)
	for {
		err := os.Mkdir(path, 0700)
		switch {
		case err == nil:
			return true, nil
		case os.IsExist(err):
			return false, nil
		case !retryableWindowsCreateError(err), !time.Now().Before(deadline):
			return false, err
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func retryableWindowsCreateError(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
