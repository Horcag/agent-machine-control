//go:build !windows

package ssh

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPrivateKeyMaterialSupportsContainedExtensionlessKey(t *testing.T) {
	keysDir := t.TempDir()
	want := []byte("synthetic-extensionless-key")
	if err := os.WriteFile(filepath.Join(keysDir, "default"), want, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := loadPrivateKeyMaterial(keysDir, "default")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("loaded key = %q, want %q", got, want)
	}
}

func TestLoadPrivateKeyMaterialRejectsOutsideAliases(t *testing.T) {
	root := t.TempDir()
	keysDir := filepath.Join(root, "keys")
	if err := os.Mkdir(keysDir, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.key")
	if err := os.WriteFile(outside, []byte("synthetic-key"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{"../outside", `..\\outside`, "/outside", "outside.key"} {
		if _, err := loadPrivateKeyMaterial(keysDir, alias); err == nil {
			t.Errorf("loadPrivateKeyMaterial accepted alias %q outside the key store", alias)
		}
	}
}
