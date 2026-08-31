//go:build !windows

package lease

import "os"

func readTransitionLockOwner(path string) ([]byte, error) {
	return os.ReadFile(path)
}
