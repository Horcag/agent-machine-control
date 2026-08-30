//go:build windows

package approval

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func validateApprovalFilePrivacy(path string, _ os.FileInfo) error {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return fmt.Errorf("%w: cannot inspect current identity", ErrInsecurePermissions)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return fmt.Errorf("%w: cannot resolve current identity", ErrInsecurePermissions)
	}
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("%w: cannot inspect approval DACL", ErrInsecurePermissions)
	}
	owner, _, ownerErr := descriptor.Owner()
	dacl, _, daclErr := descriptor.DACL()
	if ownerErr != nil || owner == nil || !owner.Equals(user.User.Sid) || daclErr != nil || dacl == nil {
		return ErrInsecurePermissions
	}
	return nil
}
