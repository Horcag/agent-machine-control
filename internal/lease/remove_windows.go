//go:build windows

package lease

import (
	"errors"
	"time"

	"golang.org/x/sys/windows"
)

func removePathBounded(removeFn func(string) error, path string) error {
	deadline := time.Now().Add(250 * time.Millisecond)
	for {
		err := removeFn(path)
		if err == nil || !retryableWindowsRemoveError(err) || !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func retryableWindowsRemoveError(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
