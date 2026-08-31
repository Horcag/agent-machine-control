//go:build windows

package target

import (
	"context"
	"path/filepath"
	"testing"
)

func mutationJournalNewDirectorySecurityCalls() string { return "validate" }

func mutationJournalNewDirectoryProtectFailureCalls() string { return "validate" }

func mutationJournalUsesPostCreateProtection() bool { return false }

func TestCreatePlatformMutationJournalDirectoryUsesFinalPrivateContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), mutationDirName)
	created, err := createPlatformMutationJournalDirectory(context.Background(), path, newPlatformSecurity())
	if err != nil || !created {
		t.Fatalf("createPlatformMutationJournalDirectory = created %t, err %v", created, err)
	}
	if err := validateTargetWindowsACL(path, PathDirectory); err != nil {
		t.Fatalf("validate created mutation directory: %v", err)
	}
	created, err = createPlatformMutationJournalDirectory(context.Background(), path, newPlatformSecurity())
	if err != nil || created {
		t.Fatalf("second creation = created %t, err %v; want existing directory unchanged", created, err)
	}
}
