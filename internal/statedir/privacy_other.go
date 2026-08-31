//go:build !windows && !linux

package statedir

import "os"

func ensurePlatformStateDirectories([]string, string) (bool, error) { return false, nil }

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

func isWindowsHostBackedStatePath(string) (bool, error) { return false, nil }
