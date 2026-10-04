//go:build linux

package bootstrap

import (
	"os"
	"path/filepath"
	"syscall"

	"github.com/Horcag/agent-machine-control/internal/wslruntime"
)

// Prior-boot readiness requires private native files owned by this operator.
// Host-backed or otherwise unproven ownership fails closed.
func priorRuntimePathsOwned(daemonDir string, endpointPresent bool) bool {
	return priorRuntimeNativePathsOwned(daemonDir, endpointPresent, wslruntime.IsWindowsHostPath)
}

func priorRuntimeNativePathsOwned(daemonDir string, endpointPresent bool, detectHostPath func(string) (bool, error)) bool {
	paths := []string{filepath.Dir(daemonDir), daemonDir, filepath.Join(daemonDir, "singleton.lock"), filepath.Join(daemonDir, "singleton.lock", "owner.json")}
	if endpointPresent {
		paths = append(paths, filepath.Join(daemonDir, "endpoint.json"))
	}
	for _, path := range paths {
		hostBacked, err := detectHostPath(path)
		if err != nil || hostBacked {
			return false
		}
		info, err := os.Lstat(path)
		if err != nil || !priorRuntimeFileOwned(info, os.Geteuid()) {
			return false
		}
	}
	// Lstat on the records alone does not exclude symlinked ancestors.
	for path := daemonDir; ; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
		if filepath.Dir(path) == path {
			return true
		}
	}
}

func priorRuntimeFileOwned(info os.FileInfo, uid int) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uid < 0 || uint64(stat.Uid) != uint64(uid) || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	if info.IsDir() {
		return info.Mode().Perm() == 0700
	}
	return info.Mode().IsRegular() && info.Mode().Perm() == 0600
}
