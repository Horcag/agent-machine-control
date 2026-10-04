//go:build !linux

package bootstrap

// Linux prior-boot ownership cannot be proven on another operating system.
func priorRuntimePathsOwned(string, bool) bool { return false }
