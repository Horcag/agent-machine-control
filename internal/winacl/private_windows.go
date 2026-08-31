//go:build windows

// Package winacl enforces the Windows ACL contract for private state files.
package winacl

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	// ErrInsecureFile indicates that a file does not satisfy the private-state ACL contract.
	ErrInsecureFile = errors.New("windows private file has insecure ACL")
)

const privateFileFullAccess = 0x001f01ff

// ValidatePrivateFile requires the current user to own path and requires a protected DACL
// with exactly one full-control allow entry for the owner, LocalSystem, and Builtin Administrators.
func ValidatePrivateFile(path string) error {
	ownerSID, err := currentUserSID()
	if err != nil {
		return fmt.Errorf("%w: current identity: %v", ErrInsecureFile, err)
	}
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil || descriptor == nil {
		return fmt.Errorf("%w: inspect security descriptor", ErrInsecureFile)
	}
	return validatePrivateFileSecurityDescriptor(descriptor, ownerSID)
}

func validatePrivateFileSecurityDescriptor(descriptor *windows.SECURITY_DESCRIPTOR, ownerSID *windows.SID) error {
	if descriptor == nil || ownerSID == nil {
		return fmt.Errorf("%w: inspect security descriptor", ErrInsecureFile)
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || !owner.Equals(ownerSID) {
		return fmt.Errorf("%w: unexpected owner", ErrInsecureFile)
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("%w: DACL is not protected", ErrInsecureFile)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("%w: DACL is unavailable", ErrInsecureFile)
	}

	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return fmt.Errorf("%w: resolve LocalSystem SID", ErrInsecureFile)
	}
	administratorsSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return fmt.Errorf("%w: resolve Administrators SID", ErrInsecureFile)
	}
	allowed := distinctSIDs(ownerSID, systemSID, administratorsSID)
	if dacl.AceCount != uint16(len(allowed)) {
		return fmt.Errorf("%w: unexpected DACL entry count", ErrInsecureFile)
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil || ace == nil {
			return fmt.Errorf("%w: inspect DACL entry", ErrInsecureFile)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("%w: unexpected DACL entry type", ErrInsecureFile)
		}
		if ace.Header.AceFlags != windows.NO_INHERITANCE || ace.Mask != privateFileFullAccess {
			return fmt.Errorf("%w: unexpected DACL entry permissions", ErrInsecureFile)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.Equals(allowed[index]) {
			return fmt.Errorf("%w: unexpected DACL entry principal", ErrInsecureFile)
		}
	}
	return nil
}

// ProtectPrivateFile replaces a current-user-owned path's DACL with protected full-control
// entries for the current owner, LocalSystem, and Builtin Administrators. It rejects a foreign
// existing owner rather than taking ownership of an object it did not create.
func ProtectPrivateFile(path string) error {
	return protectPrivateFile(path, false)
}

// ProtectNewPrivateFile normalizes a freshly created file to the current token user before
// applying the private DACL. It must only be called immediately after exclusive file creation.
func ProtectNewPrivateFile(path string) error {
	return protectPrivateFile(path, true)
}

func protectPrivateFile(path string, normalizeOwner bool) error {
	ownerSID, err := currentUserSID()
	if err != nil {
		return fmt.Errorf("resolve current identity: %w", err)
	}
	owner, err := privateFileOwner(path)
	if err != nil {
		return err
	}
	if !normalizeOwner && !owner.Equals(ownerSID) {
		return fmt.Errorf("%w: refusing to protect a file owned by a foreign Windows SID", ErrInsecureFile)
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return fmt.Errorf("resolve LocalSystem SID: %w", err)
	}
	administratorsSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return fmt.Errorf("resolve Administrators SID: %w", err)
	}

	sids := distinctSIDs(ownerSID, systemSID, administratorsSID)
	var pinner runtime.Pinner
	defer pinner.Unpin()
	entries := make([]windows.EXPLICIT_ACCESS, 0, len(sids))
	for _, sid := range sids {
		pinner.Pin(sid)
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.ACCESS_MASK(privateFileFullAccess),
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		})
	}
	dacl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("build private DACL: %w", err)
	}
	securityInformation := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
	var ownerToSet *windows.SID
	if normalizeOwner {
		securityInformation |= windows.OWNER_SECURITY_INFORMATION
		ownerToSet = ownerSID
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		securityInformation,
		ownerToSet,
		nil,
		dacl,
		nil,
	); err != nil {
		return fmt.Errorf("apply private DACL: %w", err)
	}
	return ValidatePrivateFile(path)
}

func privateFileOwner(path string) (*windows.SID, error) {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil || descriptor == nil {
		return nil, fmt.Errorf("%w: inspect file owner", ErrInsecureFile)
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		return nil, fmt.Errorf("%w: file owner is unavailable", ErrInsecureFile)
	}
	return owner, nil
}

func currentUserSID() (*windows.SID, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return nil, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return nil, errors.New("current user SID is unavailable")
	}
	return user.User.Sid, nil
}

func sidAllowed(actual *windows.SID, allowed []*windows.SID) bool {
	if actual == nil {
		return false
	}
	for _, candidate := range allowed {
		if candidate != nil && actual.Equals(candidate) {
			return true
		}
	}
	return false
}

func distinctSIDs(candidates ...*windows.SID) []*windows.SID {
	allowed := make([]*windows.SID, 0, len(candidates))
	for _, candidate := range candidates {
		if !sidAllowed(candidate, allowed) {
			allowed = append(allowed, candidate)
		}
	}
	return allowed
}
