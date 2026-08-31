//go:build darwin

package statedir

import (
	"fmt"
	"path/filepath"
)

func allowedSystemPathAlias(path string) (string, bool, error) {
	if filepath.Clean(path) != "/var" {
		return "", false, nil
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", false, err
	}
	if filepath.Clean(resolved) != "/private/var" {
		return "", false, fmt.Errorf("%w: unexpected Darwin /var target %q", ErrSymlinkNotAllowed, resolved)
	}
	return "/private/var", true, nil
}
