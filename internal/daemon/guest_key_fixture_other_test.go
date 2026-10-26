//go:build !windows

package daemon

import (
	"path/filepath"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/statedir"
)

func writeGuestFixtureKeyMaterial(t *testing.T, sd *statedir.StateDir, material []byte) error {
	t.Helper()
	return writeGuestPrivateFixture(t, filepath.Join(sd.KeysDir(), "default.key"), material)
}
