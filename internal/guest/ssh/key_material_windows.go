//go:build windows

package ssh

import (
	"errors"
	"fmt"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

func loadPrivateKeyMaterial(keysDir, alias string) ([]byte, error) {
	path := filepath.Join(keysDir, alias+".dpapi")
	if err := validateServiceIdentityACL(path); err != nil {
		return nil, err
	}
	protected, err := validateStrictFile(path)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(protected)
	if len(protected) == 0 {
		return nil, errors.New("ssh: empty DPAPI key blob")
	}

	in := windows.DataBlob{Size: uint32(len(protected)), Data: &protected[0]}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, errors.New("ssh: DPAPI key decryption failed for the daemon service identity")
	}
	if out.Data == nil || out.Size == 0 {
		return nil, errors.New("ssh: DPAPI returned empty key material")
	}
	decryptedView := unsafe.Slice(out.Data, out.Size)
	decrypted := append([]byte(nil), decryptedView...)
	zeroBytes(decryptedView)
	_, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return decrypted, nil
}

func validateServiceIdentityACL(path string) error {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return fmt.Errorf("ssh: cannot inspect daemon service token: %w", err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return fmt.Errorf("ssh: cannot inspect daemon service identity: %w", err)
	}

	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("ssh: cannot inspect DPAPI key ACL: %w", err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(user.User.Sid) {
		return errors.New("ssh: DPAPI key file owner is not the daemon service identity")
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return errors.New("ssh: DPAPI key file has no restrictive DACL")
	}

	readMask := windows.ACCESS_MASK(windows.FILE_GENERIC_READ | windows.GENERIC_READ | windows.GENERIC_ALL)
	foundIdentityRead := false
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil || ace == nil {
			return errors.New("ssh: DPAPI key file contains an unreadable ACL entry")
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("ssh: DPAPI key file contains an unsupported permissive ACL entry")
		}
		if ace.Mask&readMask == 0 {
			continue
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !aceSID.Equals(user.User.Sid) {
			return errors.New("ssh: DPAPI key file grants read access outside the daemon service identity")
		}
		foundIdentityRead = true
	}
	if !foundIdentityRead {
		return errors.New("ssh: DPAPI key file does not grant the daemon service identity read access")
	}
	return nil
}
