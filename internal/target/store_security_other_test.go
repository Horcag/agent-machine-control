//go:build !windows

package target

import (
	"os"
	"testing"
)

func requireStoredStateSecurity(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("state mode = %v, %v", info.Mode().Perm(), err)
	}
}
