//go:build windows

package winacl

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestProtectNewPrivateFileNormalizesDefaultOwnerAndExactDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-state")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ProtectNewPrivateFile(path); err != nil {
		t.Fatalf("ProtectNewPrivateFile: %v", err)
	}
	if err := ValidatePrivateFile(path); err != nil {
		t.Fatalf("ValidatePrivateFile after fresh protection: %v", err)
	}
}

func TestProtectPrivateFileRejectsForeignDefaultOwner(t *testing.T) {
	current, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "foreign-default-owner")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	owner, err := privateFileOwner(path)
	if err != nil {
		t.Fatal(err)
	}
	if owner.Equals(current) {
		t.Skip("fresh file owner already matches the token user")
	}
	if err := ProtectPrivateFile(path); !errors.Is(err, ErrInsecureFile) {
		t.Fatalf("ProtectPrivateFile on default-owned file = %v, want insecure-file error", err)
	}
	if err := ProtectNewPrivateFile(path); err != nil {
		t.Fatalf("ProtectNewPrivateFile after default-owner rejection: %v", err)
	}
}

func TestValidatePrivateFileRejectsExtraAllowedACE(t *testing.T) {
	owner, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.SecurityDescriptorFromString("O:" + owner.String() + "D:P(A;;FA;;;" + owner.String() + ")(A;;FA;;;SY)(A;;FA;;;BA)(A;;RC;;;" + owner.String() + ")")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("extra-owner DACL: %v", err)
	}
	if dacl.AceCount != 4 {
		t.Fatalf("extra-owner DACL entry count = %d, want 4", dacl.AceCount)
	}
	if err := validatePrivateFileSecurityDescriptor(descriptor, owner); !errors.Is(err, ErrInsecureFile) {
		t.Fatalf("private descriptor with extra allowed ACE = %v, want insecure-file error", err)
	}
}
