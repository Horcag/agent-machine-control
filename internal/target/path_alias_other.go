//go:build !darwin

package target

func allowedSystemPathAlias(string) (string, bool, error) { return "", false, nil }
