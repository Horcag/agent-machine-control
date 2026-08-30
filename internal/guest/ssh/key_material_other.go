//go:build !windows

package ssh

import (
	"os"
)

func loadPrivateKeyMaterial(keysDir, alias string) ([]byte, error) {
	keyPath, err := keyMaterialPath(keysDir, alias, ".key")
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		keyPath, err = keyMaterialPath(keysDir, alias, "")
		if err != nil {
			return nil, err
		}
	}
	return validateStrictFile(keyPath)
}
