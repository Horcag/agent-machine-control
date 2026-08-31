package sessions_test

import (
	"path/filepath"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/statedir"
)

func testStateDir(t *testing.T) *statedir.StateDir {
	t.Helper()
	sd, err := statedir.Resolve(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatalf("resolve state directory: %v", err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatalf("create state directory: %v", err)
	}
	return sd
}
