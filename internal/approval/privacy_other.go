//go:build !windows

package approval

import "os"

func validateApprovalFilePrivacy(_ string, info os.FileInfo) error {
	if info.Mode().Perm()&0002 != 0 {
		return ErrInsecurePermissions
	}
	return nil
}

func createApprovalFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
}
