//go:build !windows

package ssh

import (
	"os"
	"path/filepath"
)

func loadPrivateKeyMaterial(keysDir, alias string) ([]byte, error) {
	keyPath := filepath.Join(keysDir, alias+".key")
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		keyPath = filepath.Join(keysDir, alias)
	}
	return validateStrictFile(keyPath)
}
