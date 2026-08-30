//go:build windows

package ssh

import (
	"context"
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

func loadPrivateKeyMaterial(keysDir, alias string) ([]byte, error) {
	return loadPrivateKeyMaterialContext(context.Background(), keysDir, alias)
}

func loadPrivateKeyMaterialContext(ctx context.Context, keysDir, alias string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := keyMaterialPath(keysDir, alias, ".dpapi")
	if err != nil {
		return nil, err
	}
	if err := validateServiceIdentityACL(keysDir, true); err != nil {
		return nil, err
	}
	if err := validateServiceIdentityACL(path, false); err != nil {
		return nil, err
	}
	protected, err := validateStrictFileContext(ctx, path)
	if err != nil {
		return nil, err
	}
	defer zeroBytes(protected)
	if len(protected) == 0 {
		return nil, errors.New("ssh: empty DPAPI key blob")
	}

	in := windows.DataBlob{Size: uint32(len(protected)), Data: &protected[0]}
	var out windows.DataBlob
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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

func validateServiceIdentityACL(path string, directory bool) error {
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
	ownerMatches := err == nil && owner != nil && owner.Equals(user.User.Sid)
	if err := validateKeyACLMetadata(ownerMatches, true); err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err := validateKeyACLMetadata(true, err == nil && dacl != nil); err != nil {
		return err
	}

	var identityMask windows.ACCESS_MASK
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil || ace == nil {
			return errors.New("ssh: protected key path contains an unreadable ACL entry")
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		serviceIdentity := aceSID.Equals(user.User.Sid)
		if err := validateKeyACLEntry(ace.Header.AceType, ace.Mask, serviceIdentity); err != nil {
			return err
		}
		if ace.Header.AceType == windows.ACCESS_ALLOWED_ACE_TYPE && serviceIdentity {
			identityMask |= ace.Mask
		}
	}
	if err := validateRequiredServiceKeyRights(identityMask, directory); err != nil {
		return err
	}
	return nil
}

func validateKeyACLMetadata(ownerMatches, daclPresent bool) error {
	if !ownerMatches {
		return errors.New("ssh: protected key path owner is not the daemon service identity")
	}
	if !daclPresent {
		return errors.New("ssh: protected key path has no restrictive DACL")
	}
	return nil
}

func validateRequiredServiceKeyRights(identityMask windows.ACCESS_MASK, directory bool) error {
	required := requiredServiceKeyRights(directory)
	if identityMask&required != required {
		return errors.New("ssh: protected key path does not grant required daemon service access")
	}
	return nil
}

const fileDeleteChild windows.ACCESS_MASK = 0x00000040

func materialKeyRights() windows.ACCESS_MASK {
	return windows.ACCESS_MASK(
		windows.FILE_GENERIC_READ |
			windows.FILE_GENERIC_WRITE |
			windows.FILE_GENERIC_EXECUTE |
			windows.DELETE |
			windows.WRITE_DAC |
			windows.WRITE_OWNER |
			windows.GENERIC_ALL |
			windows.GENERIC_READ |
			windows.GENERIC_WRITE |
			windows.GENERIC_EXECUTE |
			fileDeleteChild,
	)
}

func requiredServiceKeyRights(directory bool) windows.ACCESS_MASK {
	if directory {
		return windows.ACCESS_MASK(windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE) | fileDeleteChild
	}
	return windows.ACCESS_MASK(windows.FILE_GENERIC_READ)
}

func validateKeyACLEntry(aceType uint8, mask windows.ACCESS_MASK, serviceIdentity bool) error {
	switch aceType {
	case windows.ACCESS_DENIED_ACE_TYPE:
		if serviceIdentity && mask&materialKeyRights() != 0 {
			return errors.New("ssh: protected key path denies required daemon service access")
		}
		return nil
	case windows.ACCESS_ALLOWED_ACE_TYPE:
		if !serviceIdentity && mask&materialKeyRights() != 0 {
			return errors.New("ssh: protected key path grants material access outside the daemon service identity")
		}
		return nil
	default:
		return errors.New("ssh: protected key path contains an unsupported permissive ACL entry")
	}
}
