//go:build !windows

package statedir

func ensurePlatformPrivateDirectory(string) error { return nil }
