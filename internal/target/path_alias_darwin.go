//go:build darwin

package target

import "path/filepath"

func allowedSystemPathAlias(path string) (string, bool, error) {
	return verifiedDarwinVarAlias(path, filepath.EvalSymlinks)
}
