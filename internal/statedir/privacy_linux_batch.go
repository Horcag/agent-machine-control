//go:build linux

package statedir

import (
	"context"
	"fmt"
	"os"
	"slices"
)

func ensurePlatformStateDirectories(paths []string, targetsPath string) (bool, error) {
	if len(paths) == 0 {
		return false, nil
	}
	hostBacked, err := isWindowsHostBackedStatePath(paths[0])
	if err != nil {
		return true, fmt.Errorf("%w: determine state-directory filesystem: %v", ErrInsecurePermissions, err)
	}
	if !hostBacked {
		return false, nil
	}

	requests, err := windowsHostStateDirRequests(paths, targetsPath)
	if err != nil {
		return true, err
	}
	if _, err := windowsStateDirBatchGuard(context.Background(), requests); err != nil {
		return true, fmt.Errorf("%w: ensure Windows-host-backed state directories: %v", ErrInsecurePermissions, err)
	}
	return true, nil
}

func windowsHostStateDirRequests(paths []string, targetsPath string) ([]windowsHostStateDirRequest, error) {
	root := paths[0]
	missing, err := missingStateDirComponents(root, false, func(path string, info os.FileInfo, _ bool) error {
		if err := validateNoSymlinkComponents(path); err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("state path %q exists and is not a directory", path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	ordered := make([]string, 0, len(missing)+len(paths))
	for _, path := range slices.Backward(missing) {
		ordered = append(ordered, path)
	}
	ordered = append(ordered, paths...)

	requests := make([]windowsHostStateDirRequest, 0, len(ordered))
	seen := make(map[string]struct{}, len(ordered))
	for _, path := range ordered {
		if _, duplicate := seen[path]; duplicate {
			continue
		}
		seen[path] = struct{}{}
		hostBacked, err := isWindowsHostBackedStatePath(path)
		if err != nil {
			return nil, fmt.Errorf("%w: determine state-directory filesystem: %v", ErrInsecurePermissions, err)
		}
		if !hostBacked {
			return nil, fmt.Errorf("%w: state-directory tree crosses a non-Windows mount", ErrInsecurePermissions)
		}
		requests = append(requests, windowsHostStateDirRequest{
			Path:                   path,
			Action:                 "create",
			AllowTargetInheritance: path == targetsPath,
		})
	}
	return requests, nil
}
