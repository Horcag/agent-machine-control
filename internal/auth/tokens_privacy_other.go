//go:build !windows

package auth

import (
	"fmt"
	"os"
	"path/filepath"
)

func validateTokenFilePrivacy(path string, info os.FileInfo) error {
	if info.Mode().Perm() != 0600 {
		return fmt.Errorf("auth: token file %s has insecure permissions %04o; must be 0600", filepath.Base(path), info.Mode().Perm())
	}
	return nil
}

func protectTokenFile(string) error { return nil }
