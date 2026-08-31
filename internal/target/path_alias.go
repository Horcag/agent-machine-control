package target

import (
	"fmt"
	"path/filepath"
)

func verifiedDarwinVarAlias(path string, evalSymlinks func(string) (string, error)) (string, bool, error) {
	cleaned := filepath.Clean(path)
	if cleaned != "/var" {
		return "", false, nil
	}
	resolved, err := evalSymlinks(cleaned)
	if err != nil {
		return "", false, err
	}
	if filepath.Clean(resolved) != "/private/var" {
		return "", false, fmt.Errorf("unexpected Darwin /var target %q", resolved)
	}
	return "/private/var", true, nil
}
