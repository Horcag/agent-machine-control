//go:build windows

package approval_test

import "github.com/Horcag/agent-machine-control/internal/winacl"

func protectApprovalFixture(path string) error {
	return winacl.ProtectPrivateFile(path)
}
