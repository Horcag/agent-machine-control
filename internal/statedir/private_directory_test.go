package statedir

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateEvidenceDirectoryRejectsSymlinkAndCreatesProtectedDirectory(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "evidence")
	if err := EnsurePrivateDirectory(path); err != nil {
		t.Fatal(err)
	}
	if err := validatePlatformPrivateDirectory(path, true); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDirectory(path); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "evidence-link")
	if err := os.Symlink(path, link); err != nil {
		t.Skipf("symlink privilege unavailable: %v", err)
	}
	if err := EnsurePrivateDirectory(link); err == nil {
		t.Fatal("symlink evidence directory accepted")
	}
}
