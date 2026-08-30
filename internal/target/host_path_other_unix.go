//go:build unix && !linux

package target

func detectWindowsHostPath(string) (bool, error) { return false, nil }

func newPowerShellWindowsGuard() WindowsPathGuard { return nil }
