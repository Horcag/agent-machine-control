//go:build !darwin

package statedir

func allowedSystemPathAlias(string) (string, bool, error) { return "", false, nil }
