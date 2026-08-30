//go:build windows

package approval_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/winacl"
	"golang.org/x/sys/windows"
)

func TestWindowsApprovalFilesRejectBroadExplicitAccess(t *testing.T) {
	principals := []struct {
		name   string
		typeID windows.WELL_KNOWN_SID_TYPE
	}{
		{name: "Everyone", typeID: windows.WinWorldSid},
		{name: "Builtin Users", typeID: windows.WinBuiltinUsersSid},
		{name: "Authenticated Users", typeID: windows.WinAuthenticatedUserSid},
	}
	for index, principal := range principals {
		t.Run(principal.name, func(t *testing.T) {
			dir := t.TempDir()
			store := approval.NewStore(dir)
			issued := windowsTestApproval(index)
			if err := store.Issue(issued); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, string(issued.ID)+".issued.json")
			broadSID, err := windows.CreateWellKnownSid(principal.typeID)
			if err != nil {
				t.Fatal(err)
			}
			setApprovalWindowsTestACL(t, path, broadSID, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.NO_INHERITANCE, true)
			if _, err := approval.LoadFromFile(path); !errors.Is(err, approval.ErrInsecurePermissions) {
				t.Fatalf("LoadFromFile error = %v, want ErrInsecurePermissions", err)
			}
		})
	}
}

func TestWindowsApprovalInheritedBroadACLRejectedAndStoreRecordsProtected(t *testing.T) {
	dir := t.TempDir()
	broadSID, err := windows.CreateWellKnownSid(windows.WinAuthenticatedUserSid)
	if err != nil {
		t.Fatal(err)
	}
	setApprovalWindowsTestACL(t, dir, broadSID, windows.GENERIC_READ, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT, true)

	rawApproval := windowsTestApproval(10)
	rawData, err := json.Marshal(approval.ConvertToDTO(rawApproval))
	if err != nil {
		t.Fatal(err)
	}
	rawPath := filepath.Join(dir, string(rawApproval.ID)+".issued.json")
	if err := os.WriteFile(rawPath, rawData, 0600); err != nil {
		t.Fatal(err)
	}
	if !hasApprovalInheritedAllowACE(t, rawPath, broadSID) {
		t.Fatal("test approval did not inherit the broad ACE")
	}
	if _, err := approval.LoadFromFile(rawPath); !errors.Is(err, approval.ErrInsecurePermissions) {
		t.Fatalf("inherited broad approval error = %v, want ErrInsecurePermissions", err)
	}

	store := approval.NewStore(dir)
	issued := windowsTestApproval(11)
	if err := store.Issue(issued); err != nil {
		t.Fatal(err)
	}
	issuedPath := filepath.Join(dir, string(issued.ID)+".issued.json")
	if err := winacl.ValidatePrivateFile(issuedPath); err != nil {
		t.Fatalf("issued approval did not receive private DACL: %v", err)
	}
	if err := store.ValidateIssuedContext(t.Context(), issued); err != nil {
		t.Fatalf("private issued approval did not load: %v", err)
	}
	if err := store.MarkConsumed(issued, time.Date(2026, 8, 30, 12, 5, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	consumedPath := filepath.Join(dir, string(issued.ID)+".json")
	if err := winacl.ValidatePrivateFile(consumedPath); err != nil {
		t.Fatalf("consumed approval did not receive private DACL: %v", err)
	}
	if _, err := approval.LoadFromFile(consumedPath); err != nil {
		t.Fatalf("private consumed approval did not load: %v", err)
	}
}

func TestWindowsApprovalPrivateOwnerSystemAdministratorsCaseLoads(t *testing.T) {
	dir := t.TempDir()
	store := approval.NewStore(dir)
	issued := windowsTestApproval(20)
	if err := store.Issue(issued); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, string(issued.ID)+".issued.json")
	if err := winacl.ValidatePrivateFile(path); err != nil {
		t.Fatal(err)
	}
	if _, err := approval.LoadFromFile(path); err != nil {
		t.Fatal(err)
	}
}

func windowsTestApproval(index int) domain.Approval {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	return domain.Approval{
		ID:    domain.ApprovalID("app-windows-privacy-" + string(rune('a'+index))),
		Actor: "operator:windows-private", Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		AuthorizedClass: domain.ClassDestructivePrivileged,
		Fingerprint:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		IdempotencyKey:  "windows-private-approval", IssuedAt: now, ExpiresAt: now.Add(time.Hour),
	}
}

func setApprovalWindowsTestACL(t *testing.T, path string, broadSID *windows.SID, broadMask windows.ACCESS_MASK, inheritance uint32, protected bool) {
	t.Helper()
	ownerSID := currentApprovalWindowsTestSID(t)
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
			AccessPermissions: mask, AccessMode: windows.SET_ACCESS, Inheritance: inheritance,
			Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeValue: windows.TrusteeValueFromSID(sid)},
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

func currentApprovalWindowsTestSID(t *testing.T) *windows.SID {
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

func hasApprovalInheritedAllowACE(t *testing.T, path string, expectedSID *windows.SID) bool {
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
