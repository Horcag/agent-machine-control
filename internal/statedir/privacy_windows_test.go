//go:build windows

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
	if err := validatePlatformPrivateDirectory(path, false); err != nil {
		t.Fatalf("validate created directory: %v", err)
	}
	if err := createPlatformPrivateDirectory(path, false); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second creation = %v, want already exists", err)
	}
}
