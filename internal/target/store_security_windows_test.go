//go:build windows

package target

import "testing"

func requireStoredStateSecurity(t *testing.T, path string) {
	t.Helper()
	if err := validateTargetWindowsACL(path, PathFile); err != nil {
		t.Fatalf("state ACL = %v", err)
	}
}
