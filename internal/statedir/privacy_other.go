//go:build !windows

package statedir

import "os"

func createPlatformPrivateDirectory(path string, _ bool) error {
	if err := os.Mkdir(path, DirPerm); err != nil {
		return err
	}
	if err := os.Chmod(path, DirPerm); err != nil {
		return err
	}
	return nil
}

func validatePlatformPrivateDirectory(string, bool) error { return nil }
