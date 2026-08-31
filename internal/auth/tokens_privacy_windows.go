//go:build windows

package auth

import (
	"fmt"
	"os"

	"github.com/Horcag/agent-machine-control/internal/winacl"
)

func validateTokenFilePrivacy(path string, _ os.FileInfo) error {
	if err := winacl.ValidatePrivateFile(path); err != nil {
		return fmt.Errorf("auth: token file has insecure Windows permissions: %w", err)
	}
	return nil
}

func protectTokenFile(path string) error {
	if err := winacl.ProtectNewPrivateFile(path); err != nil {
		return fmt.Errorf("auth: failed to protect token file: %w", err)
	}
	return nil
}
