//go:build windows

package statedir

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

func ensurePlatformPrivateDirectory(path string) error {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return fmt.Errorf("%w: cannot inspect current identity: %v", ErrInsecurePermissions, err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return fmt.Errorf("%w: cannot resolve current identity", ErrInsecurePermissions)
	}
	sddl := "D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FA;;;SY)(A;;FA;;;BA)"
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("%w: cannot construct private DACL: %v", ErrInsecurePermissions, err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("%w: private DACL is unavailable", ErrInsecurePermissions)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		user.User.Sid, nil, dacl, nil); err != nil {
		return fmt.Errorf("%w: cannot apply private DACL to %q: %v", ErrInsecurePermissions, path, err)
	}
	return validatePlatformPrivateDirectory(path, user.User.Sid)
}

func validatePlatformPrivateDirectory(path string, expectedOwner *windows.SID) error {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("%w: cannot inspect DACL for %q: %v", ErrInsecurePermissions, path, err)
	}
	owner, _, ownerErr := descriptor.Owner()
	dacl, _, daclErr := descriptor.DACL()
	control, _, controlErr := descriptor.Control()
	if ownerErr != nil || owner == nil || expectedOwner == nil || !owner.Equals(expectedOwner) {
		return fmt.Errorf("%w: state directory owner mismatch", ErrInsecurePermissions)
	}
	if daclErr != nil || dacl == nil {
		return fmt.Errorf("%w: state directory has no restrictive DACL", ErrInsecurePermissions)
	}
	if controlErr != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("%w: state directory DACL inheritance is not protected", ErrInsecurePermissions)
	}
	return errors.Join(ownerErr, daclErr, controlErr)
}
