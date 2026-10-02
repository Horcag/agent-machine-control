//go:build !windows

package winlauncher

import "errors"

func launch(_ []string) (int, error) {
	return 1, errors.New("bootstrap launcher requires Windows")
}
