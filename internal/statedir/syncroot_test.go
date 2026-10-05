package statedir_test

import (
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSyncRootUsesOpenedDirectoryAfterPathReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows prevents renaming an open directory")
	}
	base := t.TempDir()
	path := filepath.Join(base, "original")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Rename(path, filepath.Join(base, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := statedir.SyncRoot(root); err != nil {
		t.Fatalf("sync opened directory: %v", err)
	}
}

func TestSyncRootRefusesClosedRoot(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if err := statedir.SyncRoot(root); err == nil {
		t.Fatal("closed root sync succeeded")
	}
}

func TestSyncRootValidDirectory(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := statedir.SyncRoot(root); err != nil {
		t.Fatal(err)
	}
}
