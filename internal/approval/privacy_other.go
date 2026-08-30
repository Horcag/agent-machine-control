//go:build !windows

package approval

import "os"

func validateApprovalFilePrivacy(_ string, info os.FileInfo) error {
	if info.Mode().Perm()&0002 != 0 {
		return ErrInsecurePermissions
	}
	return nil
}
