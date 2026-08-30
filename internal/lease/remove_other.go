//go:build !windows

package lease

func removePathBounded(removeFn func(string) error, path string) error { return removeFn(path) }
