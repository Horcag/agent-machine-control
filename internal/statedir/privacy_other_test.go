//go:build !windows

package statedir

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCreatePlatformPrivateDirectoryUsesFinalPrivateContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")

	if err := createPlatformPrivateDirectory(path, false); err != nil {
		t.Fatalf("createPlatformPrivateDirectory: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat created directory: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("created path is not a directory")
	}
	if got := info.Mode().Perm(); got != DirPerm {
		t.Fatalf("created directory mode = %04o, want %04o", got, DirPerm)
	}
	if err := validatePlatformPrivateDirectory(path, false); err != nil {
		t.Fatalf("validatePlatformPrivateDirectory: %v", err)
	}
	if err := createPlatformPrivateDirectory(path, false); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second creation = %v, want already exists", err)
	}
}
