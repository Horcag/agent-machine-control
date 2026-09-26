package daemon

import (
	"fmt"
	"os"
)

// The guard is persistent: unlinking it would let processes lock different file objects.
func lockSingletonGuard(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil {
		var pathInfo os.FileInfo
		pathInfo, err = os.Lstat(path)
		if err == nil && (!info.Mode().IsRegular() || !pathInfo.Mode().IsRegular() || !os.SameFile(info, pathInfo)) {
			err = fmt.Errorf("singleton guard is not a stable regular file")
		}
	}
	if err == nil {
		err = lockGuardFile(f)
	}
	if err == nil {
		var lockedPathInfo os.FileInfo
		lockedPathInfo, err = os.Lstat(path)
		if err == nil && (!lockedPathInfo.Mode().IsRegular() || !os.SameFile(info, lockedPathInfo)) {
			err = fmt.Errorf("singleton guard changed while acquiring lock")
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
