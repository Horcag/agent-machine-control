//go:build windows

package winlauncher

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSystemPowerShellIgnoresEnvironment(t *testing.T) {
	want, err := systemPowerShell()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// Environment-based resolution would select this nonexistent installation.
	t.Setenv("SystemRoot", dir)
	t.Setenv("PATH", dir)
	got, err := systemPowerShell()
	if err != nil || got != want {
		t.Fatalf("PowerShell path = (%q, %v), want %q", got, err, want)
	}
}

func TestRunSystemPowerShell(t *testing.T) {
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "wrapper with spaces.ps1")
	if err := os.WriteFile(wrapper, []byte("exit 23\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, err := Run(bootstrapArgs(wrapper))
	if code != 23 || err != nil {
		t.Fatalf("PowerShell wrapper exit = (%d, %v), want (23, nil)", code, err)
	}
}
