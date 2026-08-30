//go:build windows

package ssh

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestValidateKeyACLEntryRejectsMaterialNonServiceRights(t *testing.T) {
	t.Parallel()
	rights := []windows.ACCESS_MASK{
		windows.FILE_READ_DATA,
		windows.FILE_WRITE_DATA,
		windows.FILE_APPEND_DATA,
		windows.FILE_EXECUTE,
		windows.DELETE,
		fileDeleteChild,
		windows.WRITE_DAC,
		windows.WRITE_OWNER,
		windows.GENERIC_ALL,
		windows.GENERIC_READ,
		windows.GENERIC_WRITE,
		windows.GENERIC_EXECUTE,
	}
	for _, right := range rights {
		if err := validateKeyACLEntry(windows.ACCESS_ALLOWED_ACE_TYPE, right, false); err == nil {
			t.Errorf("non-service allow mask %#x was accepted", right)
		}
	}
}

func TestValidateKeyACLEntryTreatsInheritedAllowAsMaterial(t *testing.T) {
	t.Parallel()
	// Inheritance is carried by ACE flags and does not weaken the mask policy.
	if err := validateKeyACLEntry(windows.ACCESS_ALLOWED_ACE_TYPE, windows.FILE_GENERIC_WRITE, false); err == nil {
		t.Fatal("inherited-equivalent non-service write grant was accepted")
	}
}

func TestValidateKeyACLEntryServiceLeastPrivilege(t *testing.T) {
	t.Parallel()
	if err := validateKeyACLEntry(windows.ACCESS_ALLOWED_ACE_TYPE, requiredServiceKeyRights(false), true); err != nil {
		t.Fatalf("least-privilege service file rights rejected: %v", err)
	}
	if err := validateKeyACLEntry(windows.ACCESS_ALLOWED_ACE_TYPE, requiredServiceKeyRights(true), true); err != nil {
		t.Fatalf("least-privilege service directory rights rejected: %v", err)
	}
	if err := validateKeyACLEntry(windows.ACCESS_DENIED_ACE_TYPE, windows.FILE_GENERIC_READ, true); err == nil {
		t.Fatal("service read denial was accepted")
	}
	if err := validateKeyACLEntry(0xff, windows.FILE_GENERIC_READ, false); err == nil {
		t.Fatal("unsupported permissive ACE type was accepted")
	}
	if err := validateRequiredServiceKeyRights(requiredServiceKeyRights(false), false); err != nil {
		t.Fatalf("least-privilege file rights rejected: %v", err)
	}
	if err := validateRequiredServiceKeyRights(requiredServiceKeyRights(true), true); err != nil {
		t.Fatalf("least-privilege directory rights rejected: %v", err)
	}
	if err := validateRequiredServiceKeyRights(0, false); err == nil {
		t.Fatal("missing service file rights were accepted")
	}
}

func TestValidateKeyACLMetadataFailsClosed(t *testing.T) {
	t.Parallel()
	if err := validateKeyACLMetadata(false, true); err == nil {
		t.Fatal("wrong owner was accepted")
	}
	if err := validateKeyACLMetadata(true, false); err == nil {
		t.Fatal("null or missing DACL was accepted")
	}
	if err := validateKeyACLMetadata(true, true); err != nil {
		t.Fatalf("valid owner and DACL metadata rejected: %v", err)
	}
}
