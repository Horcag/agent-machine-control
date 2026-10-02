//go:build !windows

package winlauncher

import (
	"path/filepath"
	"testing"
)

func TestRunRefusesUnsupportedHostBeforeStartingPowerShell(t *testing.T) {
	code, err := Run(bootstrapArgs(filepath.Join(t.TempDir(), "wrapper.ps1")))
	if code != 1 || err == nil || err.Error() != "bootstrap launcher requires Windows" {
		t.Fatalf("unsupported host returned (%d, %v)", code, err)
	}
}
