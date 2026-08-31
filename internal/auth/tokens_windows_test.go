//go:build windows

package auth_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/winacl"
	"golang.org/x/sys/windows"
)

func TestWindowsTokenFilesRejectBroadExplicitAccess(t *testing.T) {
	principals := []struct {
		name   string
		typeID windows.WELL_KNOWN_SID_TYPE
	}{
		{name: "Everyone", typeID: windows.WinWorldSid},
		{name: "Builtin Users", typeID: windows.WinBuiltinUsersSid},
		{name: "Authenticated Users", typeID: windows.WinAuthenticatedUserSid},
	}
	for _, principal := range principals {
		t.Run(principal.name, func(t *testing.T) {
			dir := t.TempDir()
			if _, err := auth.LoadOrCreate(dir); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, auth.OperatorTokenFileName)
			broadSID, err := windows.CreateWellKnownSid(principal.typeID)
			if err != nil {
				t.Fatal(err)
			}
			setWindowsTestACL(t, path, broadSID, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.NO_INHERITANCE, true)
			if _, err := auth.LoadOrCreate(dir); err == nil {
				t.Fatal("LoadOrCreate accepted a token readable or writable by a broad principal")
			}
		})
	}
}

func TestWindowsTokenFilesRejectInheritedBroadAccess(t *testing.T) {
	dir := t.TempDir()
	broadSID, err := windows.CreateWellKnownSid(windows.WinAuthenticatedUserSid)
	if err != nil {
		t.Fatal(err)
	}
	setWindowsTestACL(t, dir, broadSID, windows.GENERIC_READ, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT, true)
	for index, name := range []string{auth.OperatorTokenFileName, auth.AgentMCPTokenFileName} {
		content := strings.Repeat(string(rune('a'+index)), 64) + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, auth.OperatorTokenFileName)
	if !hasInheritedAllowACE(t, path, broadSID) {
		t.Fatal("test token did not inherit the broad ACE")
	}
	if _, err := auth.LoadOrCreate(dir); err == nil {
		t.Fatal("LoadOrCreate accepted a token with an inherited broad ACE")
	}
}

func TestWindowsGeneratedTokenFilesUsePrivateDACL(t *testing.T) {
	dir := t.TempDir()
	if _, err := auth.LoadOrCreate(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{auth.OperatorTokenFileName, auth.AgentMCPTokenFileName} {
		path := filepath.Join(dir, name)
		if err := winacl.ValidatePrivateFile(path); err != nil {
			t.Fatalf("generated token %s is not private: %v", name, err)
		}
	}
	if _, err := auth.LoadOrCreate(dir); err != nil {
		t.Fatalf("private generated tokens did not reload: %v", err)
	}
}

func setWindowsTestACL(t *testing.T, path string, broadSID *windows.SID, broadMask windows.ACCESS_MASK, inheritance uint32, protected bool) {
	t.Helper()
	ownerSID := currentWindowsTestSID(t)
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	administratorsSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	sids := []*windows.SID{ownerSID, systemSID, administratorsSID, broadSID}
	var pinner runtime.Pinner
	defer pinner.Unpin()
	entries := make([]windows.EXPLICIT_ACCESS, 0, len(sids))
	for index, sid := range sids {
		pinner.Pin(sid)
		mask := windows.ACCESS_MASK(windows.GENERIC_ALL)
		if index == len(sids)-1 {
			mask = broadMask
		}
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: mask,
			AccessMode:        windows.SET_ACCESS,
			Inheritance:       inheritance,
			Trustee:           windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeValue: windows.TrusteeValueFromSID(sid)},
		})
	}
	dacl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		t.Fatal(err)
	}
	securityInfo := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION)
	if protected {
		securityInfo |= windows.PROTECTED_DACL_SECURITY_INFORMATION
	} else {
		securityInfo |= windows.UNPROTECTED_DACL_SECURITY_INFORMATION
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, securityInfo, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}

func currentWindowsTestSID(t *testing.T) *windows.SID {
	t.Helper()
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		t.Fatalf("current SID unavailable: %v", err)
	}
	return user.User.Sid
}

func hasInheritedAllowACE(t *testing.T, path string, expectedSID *windows.SID) bool {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("DACL unavailable: %v", err)
	}
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, index, &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERITED_ACE == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if sid.Equals(expectedSID) {
			return true
		}
	}
	return false
}
