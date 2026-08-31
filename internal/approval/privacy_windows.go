//go:build windows

package approval

import (
	"fmt"
	"os"

	"github.com/Horcag/agent-machine-control/internal/winacl"
)

func validateApprovalFilePrivacy(path string, _ os.FileInfo) error {
	if err := winacl.ValidatePrivateFile(path); err != nil {
		return fmt.Errorf("%w: %v", ErrInsecurePermissions, err)
	}
	return nil
}

func createApprovalFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	if err := winacl.ProtectNewPrivateFile(path); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("%w: cannot protect approval file", ErrInsecurePermissions)
	}
	return file, nil
}
