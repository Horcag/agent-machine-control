package statedir

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateAndValidateDirDoesNotNormalizeConcurrentCreator(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	var protected []string
	err := createAndValidateDirWith(dir, false, func(path string, _ bool) error {
		if err := os.Mkdir(path, DirPerm); err != nil {
			t.Fatal(err)
		}
		return os.ErrExist
	}, acceptDirectory)
	if err != nil {
		t.Fatalf("createAndValidateDirWith: %v", err)
	}
	if len(protected) != 0 {
		t.Fatalf("concurrently created directory was normalized: %v", protected)
	}
}

func TestCreateAndValidateDirProtectsEachNewNestedComponent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "new-parent")
	dir := filepath.Join(parent, "state")
	var protected []string
	err := createAndValidateDirWith(dir, false, func(path string, _ bool) error {
		if err := os.Mkdir(path, DirPerm); err != nil {
			return err
		}
		protected = append(protected, path)
		return nil
	}, acceptDirectory)
	if err != nil {
		t.Fatalf("createAndValidateDirWith: %v", err)
	}
	if len(protected) != 2 || protected[0] != parent || protected[1] != dir {
		t.Fatalf("fresh protected paths = %v, want [%q %q]", protected, parent, dir)
	}
}

func TestCreateAndValidateDirDoesNotProtectConcurrentDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	err := createAndValidateDirWith(dir, false, func(path string, _ bool) error {
		if err := os.Mkdir(path, 0755); err != nil {
			t.Fatal(err)
		}
		return os.ErrExist
	}, acceptDirectory)
	if err != nil {
		t.Fatalf("concurrent directory validation = %v", err)
	}
}

func acceptDirectory(_ string, info os.FileInfo, _ bool) error {
	if !info.IsDir() {
		return os.ErrInvalid
	}
	return nil
}
