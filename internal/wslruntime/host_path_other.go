//go:build !linux

package wslruntime

// IsWindowsHostPath is false on platforms without WSL Windows-drive mounts.
func IsWindowsHostPath(string) (bool, error) { return false, nil }
