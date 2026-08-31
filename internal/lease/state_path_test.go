package lease

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateFilePathRejectsUnsafeDerivedFilenamesWithoutFilesystemEffects(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)

	for _, tc := range []struct {
		name      string
		machineID string
		suffix    string
	}{
		{name: "parent lease", machineID: "../outside", suffix: ".lease.json"},
		{name: "absolute generation", machineID: "/outside", suffix: ".gen.json"},
		{name: "nested lease", machineID: "nested/child", suffix: ".lease.json"},
		{name: "nested generation", machineID: "nested/child", suffix: ".gen.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := mgr.stateFilePath(tc.machineID, tc.suffix); err == nil {
				t.Fatalf("stateFilePath(%q, %q) unexpectedly succeeded", tc.machineID, tc.suffix)
			}
		})
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("unsafe state paths created filesystem effects: %v", entries)
	}
}

func TestStateFilePathPreservesValidLeaseAndGenerationNames(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	machineID := "a0b1c2d3-e4f5-6789-abcd-ef0123456789"

	leasePath, err := mgr.leasePath(machineID)
	if err != nil {
		t.Fatalf("leasePath() error = %v", err)
	}
	if want := filepath.Join(dir, machineID+".lease.json"); leasePath != want {
		t.Errorf("leasePath() = %q, want %q", leasePath, want)
	}

	genPath, err := mgr.genPath(machineID)
	if err != nil {
		t.Fatalf("genPath() error = %v", err)
	}
	if want := filepath.Join(dir, machineID+".gen.json"); genPath != want {
		t.Errorf("genPath() = %q, want %q", genPath, want)
	}
}
