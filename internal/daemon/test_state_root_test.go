package daemon

import (
	"path/filepath"
	"testing"
)

func missingDaemonStateRoot(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "state")
}
