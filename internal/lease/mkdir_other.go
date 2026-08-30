//go:build !windows

package lease

import "os"

func createTransitionLockDir(path string) (bool, error) {
	err := os.Mkdir(path, 0700)
	if err == nil {
		return true, nil
	}
	if os.IsExist(err) {
		return false, nil
	}
	return false, err
}
