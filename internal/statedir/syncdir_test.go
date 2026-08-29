package statedir_test

import (
	"path/filepath"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/statedir"
)

func TestSyncDir_ValidDirectory(t *testing.T) {
	tempDir := t.TempDir()
	if err := statedir.SyncDir(tempDir); err != nil {
		t.Fatalf("SyncDir on valid directory failed: %v", err)
	}
}

func TestSyncDir_NonExistentDirectory(t *testing.T) {
	tempDir := t.TempDir()
	nonExistent := filepath.Join(tempDir, "does-not-exist")
	if err := statedir.SyncDir(nonExistent); err == nil {
		t.Fatalf("expected error when calling SyncDir on non-existent directory")
	}
}
