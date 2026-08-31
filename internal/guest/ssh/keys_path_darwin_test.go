//go:build darwin

package ssh

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateStrictFileContextAllowsDarwinVarAlias(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateStrictFileContext(t.Context(), path); err != nil {
		t.Fatalf("validateStrictFileContext() rejected Darwin /var alias: %v", err)
	}
}

func TestValidateStrictFileContextRejectsSymlinkAfterDarwinVarAlias(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "config.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := validateStrictFileContext(t.Context(), filepath.Join(link, "config.json")); err == nil {
		t.Fatal("validateStrictFileContext() accepted a non-system symlink after /var")
	}
}
