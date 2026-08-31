//go:build windows

package target

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

func validateTargetWindowsACL(path string, kind PathKind) error {
	if err := validateTargetWindowsObject(path, kind); err != nil {
		return err
	}
	proof, err := inspectTargetWindowsACL(path, kind)
	if err != nil {
		return err
	}
	return validateWindowsACLProof(proof)
}

func protectTargetWindowsACL(path string, kind PathKind) error {
	if kind != PathDirectory && kind != PathFile {
		return fmt.Errorf("target: cannot protect path kind %q", kind)
	}
	if err := validateTargetWindowsObject(path, kind); err != nil {
		return err
	}
	owner, current, err := targetWindowsOwnerAndCurrent(path)
	if err != nil {
		return err
	}
	if !owner.Equals(current) {
		return errors.New("target: refusing to protect a path owned by a foreign Windows SID")
	}

	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return fmt.Errorf("target: resolve LocalSystem SID: %w", err)
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return fmt.Errorf("target: resolve Administrators SID: %w", err)
	}
	inheritance := uint32(windows.NO_INHERITANCE)
	if kind == PathDirectory {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	sidsByText := map[string]*windows.SID{
		current.String():         current,
		windowsLocalSystemSID:    system,
		windowsAdministratorsSID: administrators,
	}
	allowed := windowsAllowedTrusteeSIDs(current.String())
	var pinner runtime.Pinner
	defer pinner.Unpin()
	entries := make([]windows.EXPLICIT_ACCESS, 0, len(allowed))
	for _, sidText := range allowed {
		sid := sidsByText[sidText]
		pinner.Pin(sid)
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.ACCESS_MASK(windowsFullControl),
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       inheritance,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		})
	}
	dacl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("target: build exact DACL: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		return fmt.Errorf("target: apply exact DACL: %w", err)
	}
	return validateTargetWindowsACL(path, kind)
}

func inspectTargetWindowsACL(path string, kind PathKind) (windowsACLProof, error) {
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil || descriptor == nil {
		return windowsACLProof{}, fmt.Errorf("target: inspect exact DACL: %w", err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		return windowsACLProof{}, errors.New("target: inspect exact DACL owner")
	}
	current, err := targetCurrentWindowsSID()
	if err != nil {
		return windowsACLProof{}, err
	}
	control, _, err := descriptor.Control()
	if err != nil {
		return windowsACLProof{}, fmt.Errorf("target: inspect exact DACL control: %w", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return windowsACLProof{}, errors.New("target: exact DACL is unavailable")
	}
	proof := windowsACLProof{
		Owner:       owner.String(),
		CurrentUser: current.String(),
		Protected:   control&windows.SE_DACL_PROTECTED != 0,
		Kind:        kind,
		Entries:     make([]windowsACEProof, 0, dacl.AceCount),
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil || ace == nil {
			return windowsACLProof{}, errors.New("target: inspect exact DACL entry")
		}
		entry := windowsACEProof{Type: ace.Header.AceType, Flags: ace.Header.AceFlags}
		if entry.Type == windowsACEAllow || entry.Type == windowsACEDeny {
			entry.Mask = uint32(ace.Mask)
			entry.SID = (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		}
		proof.Entries = append(proof.Entries, entry)
	}
	return proof, nil
}

func targetWindowsOwnerAndCurrent(path string) (*windows.SID, *windows.SID, error) {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil || descriptor == nil {
		return nil, nil, fmt.Errorf("target: inspect Windows owner: %w", err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil {
		return nil, nil, errors.New("target: Windows owner is unavailable")
	}
	current, err := targetCurrentWindowsSID()
	if err != nil {
		return nil, nil, err
	}
	return owner, current, nil
}

func targetCurrentWindowsSID() (*windows.SID, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return nil, fmt.Errorf("target: open current Windows token: %w", err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return nil, errors.New("target: current Windows SID is unavailable")
	}
	return user.User.Sid, nil
}
