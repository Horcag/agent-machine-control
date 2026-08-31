//go:build windows

package statedir

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

const stateDirectoryFullAccess = 0x001f01ff

// createPlatformPrivateDirectory creates a directory with its final owner and protected DACL.
// It intentionally never re-opens the pathname to establish the initial security contract.
func createPlatformPrivateDirectory(path string, _ bool) error {
	user, err := currentStateDirUser()
	if err != nil {
		return err
	}
	descriptor, err := privateStateDirSecurityDescriptor(user.User.Sid)
	if err != nil {
		return err
	}
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("%w: encode state directory path %q: %v", ErrInsecurePermissions, path, err)
	}
	attributes := windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	if err := windows.CreateDirectory(path16, &attributes); err != nil {
		return err
	}
	return nil
}

func validatePlatformPrivateDirectory(path string, allowTargetInheritance bool) error {
	user, err := currentStateDirUser()
	if err != nil {
		return err
	}
	return validatePlatformPrivateDirectoryOwner(path, user.User.Sid, allowTargetInheritance)
}

func currentStateDirUser() (*windows.Tokenuser, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return nil, fmt.Errorf("%w: cannot inspect current identity: %v", ErrInsecurePermissions, err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return nil, fmt.Errorf("%w: cannot resolve current identity", ErrInsecurePermissions)
	}
	return user, nil
}

func privateStateDirSecurityDescriptor(owner *windows.SID) (*windows.SECURITY_DESCRIPTOR, error) {
	sddl := "O:" + owner.String() + "D:P(A;;FA;;;" + owner.String() + ")(A;;FA;;;SY)(A;;FA;;;BA)"
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot construct private DACL: %v", ErrInsecurePermissions, err)
	}
	return descriptor, nil
}

func validatePlatformPrivateDirectoryOwner(path string, expectedOwner *windows.SID, allowTargetInheritance bool) error {
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
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return fmt.Errorf("%w: cannot resolve LocalSystem SID", ErrInsecurePermissions)
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return fmt.Errorf("%w: cannot resolve Administrators SID", ErrInsecurePermissions)
	}
	allowed := distinctStateDirSIDs(expectedOwner, system, administrators)
	if dacl.AceCount != uint16(len(allowed)) {
		return fmt.Errorf("%w: state directory has an unexpected DACL entry count", ErrInsecurePermissions)
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil || ace == nil {
			return fmt.Errorf("%w: cannot inspect state directory DACL entry", ErrInsecurePermissions)
		}
		validFlags := ace.Header.AceFlags == windows.NO_INHERITANCE || allowTargetInheritance && ace.Header.AceFlags == windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !validFlags || ace.Mask != stateDirectoryFullAccess {
			return fmt.Errorf("%w: state directory has an unexpected DACL entry", ErrInsecurePermissions)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.Equals(allowed[index]) {
			return fmt.Errorf("%w: state directory grants an unexpected principal", ErrInsecurePermissions)
		}
	}
	return errors.Join(ownerErr, daclErr, controlErr)
}

func distinctStateDirSIDs(candidates ...*windows.SID) []*windows.SID {
	allowed := make([]*windows.SID, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		duplicate := false
		for _, existing := range allowed {
			if candidate.Equals(existing) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			allowed = append(allowed, candidate)
		}
	}
	return allowed
}
