//go:build windows

package auth_test

import "github.com/Horcag/agent-machine-control/internal/winacl"

func protectTokenFixture(path string) error {
	return winacl.ProtectPrivateFile(path)
}
