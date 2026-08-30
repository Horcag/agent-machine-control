//go:build !windows

package approval

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateApprovalFilePrivacyPOSIXModes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "approval.json")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateApprovalFilePrivacy(path, info); err != nil {
		t.Fatalf("private file rejected: %v", err)
	}
	if err := os.Chmod(path, 0602); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateApprovalFilePrivacy(path, info); !errors.Is(err, ErrInsecurePermissions) {
		t.Fatalf("world-writable file error = %v", err)
	}
}
