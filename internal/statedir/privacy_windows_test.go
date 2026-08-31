//go:build windows

package statedir

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestPrivateStateDirSecurityDescriptorUsesExactInheritanceMode(t *testing.T) {
	user, err := currentStateDirUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name               string
		allowInheritance   bool
		wantACEInheritance byte
	}{
		{name: "non-inheritable", wantACEInheritance: windows.NO_INHERITANCE},
		{name: "target-inheritable", allowInheritance: true, wantACEInheritance: windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			descriptor, err := privateStateDirSecurityDescriptor(user.User.Sid, tc.allowInheritance)
			if err != nil {
				t.Fatal(err)
			}
			assertPrivateStateDirDescriptor(t, descriptor, user.User.Sid, tc.wantACEInheritance)
		})
	}
}

func TestPrivateStateDirSecurityDescriptorCollapsesDuplicatePrincipals(t *testing.T) {
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range []*windows.SID{system, administrators} {
		descriptor, err := privateStateDirSecurityDescriptor(owner, false)
		if err != nil {
			t.Fatal(err)
		}
		assertPrivateStateDirDescriptor(t, descriptor, owner, windows.NO_INHERITANCE)
	}
}

func TestCreatePlatformPrivateDirectoryUsesFinalPrivateContract(t *testing.T) {
	for _, allowInheritance := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "state")
		if err := createPlatformPrivateDirectory(path, allowInheritance); err != nil {
			t.Fatalf("createPlatformPrivateDirectory(%t): %v", allowInheritance, err)
		}
		if err := validatePlatformPrivateDirectory(path, allowInheritance); err != nil {
			t.Fatalf("validate created directory (%t): %v", allowInheritance, err)
		}
		if err := createPlatformPrivateDirectory(path, allowInheritance); !errors.Is(err, os.ErrExist) {
			t.Fatalf("second creation (%t) = %v, want already exists", allowInheritance, err)
		}
	}
}

func assertPrivateStateDirDescriptor(t *testing.T, descriptor *windows.SECURITY_DESCRIPTOR, owner *windows.SID, wantACEInheritance byte) {
	t.Helper()
	actualOwner, _, err := descriptor.Owner()
	if err != nil || actualOwner == nil || !actualOwner.Equals(owner) {
		t.Fatalf("descriptor owner = %v, %v; want current user", actualOwner, err)
	}
	control, _, err := descriptor.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("descriptor control = %#x, %v; want protected DACL", control, err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("descriptor DACL = %v, %v", dacl, err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	allowed := distinctStateDirSIDs(owner, system, administrators)
	if dacl.AceCount != uint16(len(allowed)) {
		t.Fatalf("DACL ACE count = %d, want %d", dacl.AceCount, len(allowed))
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil || ace == nil {
			t.Fatalf("GetAce(%d): %v", index, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != wantACEInheritance || ace.Mask != stateDirectoryFullAccess {
			t.Fatalf("ACE %d = type %#x flags %#x mask %#x; want full allow with flags %#x", index, ace.Header.AceType, ace.Header.AceFlags, ace.Mask, wantACEInheritance)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.Equals(allowed[index]) {
			t.Fatalf("ACE %d principal does not match required order", index)
		}
	}
}
