package statedir_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/statedir"
)

func TestStateDir_ResolveOrder(t *testing.T) {
	tempDir := t.TempDir()
	explicitPath := filepath.Join(tempDir, "explicit")
	envPath := filepath.Join(tempDir, "from_env")

	// 1. Explicit flag takes top priority
	t.Setenv(statedir.EnvStateDir, envPath)
	sd, err := statedir.Resolve(explicitPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sd.Root() != explicitPath {
		t.Errorf("expected explicit path %q, got %q", explicitPath, sd.Root())
	}

	// 2. Env variable takes second priority
	sd, err = statedir.Resolve("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sd.Root() != envPath {
		t.Errorf("expected env path %q, got %q", envPath, sd.Root())
	}

	// 3. Fallback to platform-appropriate default when AMC_STATE_DIR is unset
	t.Setenv(statedir.EnvStateDir, "")
	sd, err = statedir.Resolve("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sd.Root() == "" {
		t.Errorf("expected non-empty default root")
	}
}

func TestStateDir_EnsureDirs(t *testing.T) {
	tempDir := t.TempDir()
	statePath := filepath.Join(tempDir, "test-state")

	sd, err := statedir.Resolve(statePath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := sd.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs failed: %v", err)
	}

	// Verify all subdirs exist
	expectedDirs := []string{
		sd.Root(),
		sd.LeasesDir(),
		sd.ReceiptsDir(),
		sd.AuditDir(),
		sd.ApprovalsDir(),
	}

	for _, dir := range expectedDirs {
		info, err := os.Stat(dir)
		if err != nil {
			t.Errorf("expected directory %q to exist: %v", dir, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("expected %q to be a directory", dir)
		}
		// On POSIX, check permissions
		perm := info.Mode().Perm()
		if perm != statedir.DirPerm {
			t.Errorf("expected permission %o for %q, got %o", statedir.DirPerm, dir, perm)
		}
	}
}

func TestStateDir_SubdirGetters(t *testing.T) {
	root := "/test/state"
	sd, err := statedir.Resolve(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if sd.LeasesDir() != filepath.Join(root, "leases") {
		t.Errorf("expected leases dir %q, got %q", filepath.Join(root, "leases"), sd.LeasesDir())
	}
	if sd.ReceiptsDir() != filepath.Join(root, "receipts") {
		t.Errorf("expected receipts dir %q, got %q", filepath.Join(root, "receipts"), sd.ReceiptsDir())
	}
	if sd.AuditDir() != filepath.Join(root, "audit") {
		t.Errorf("expected audit dir %q, got %q", filepath.Join(root, "audit"), sd.AuditDir())
	}
	if sd.ApprovalsDir() != filepath.Join(root, "approvals") {
		t.Errorf("expected approvals dir %q, got %q", filepath.Join(root, "approvals"), sd.ApprovalsDir())
	}
}

func TestStateDir_NotADirectory(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "regular_file")
	if err := os.WriteFile(filePath, []byte("data"), 0600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	sd, err := statedir.Resolve(filePath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := sd.EnsureDirs(); err == nil {
		t.Fatalf("expected EnsureDirs to fail when state root is a regular file")
	}
}

func TestStateDir_SubdirSymlink(t *testing.T) {
	tempDir := t.TempDir()
	root := filepath.Join(tempDir, "state_root")
	_ = os.MkdirAll(root, 0700)

	realLeases := filepath.Join(tempDir, "real_leases")
	_ = os.MkdirAll(realLeases, 0700)

	symlinkLeases := filepath.Join(root, "leases")
	_ = os.Symlink(realLeases, symlinkLeases)

	sd, err := statedir.Resolve(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := sd.EnsureDirs(); err == nil {
		t.Fatalf("expected EnsureDirs to fail when a subdir is a symlink")
	}
}

func TestStateDir_RejectSymlink(t *testing.T) {
	tempDir := t.TempDir()
	realDir := filepath.Join(tempDir, "real_dir")
	if err := os.MkdirAll(realDir, 0700); err != nil {
		t.Fatalf("failed to create real dir: %v", err)
	}

	symlinkDir := filepath.Join(tempDir, "symlink_dir")
	if err := os.Symlink(realDir, symlinkDir); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	sd, err := statedir.Resolve(symlinkDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := sd.EnsureDirs(); err == nil {
		t.Fatalf("expected EnsureDirs to fail on symlinked state root")
	}
}

func TestStateDir_InsecurePermFix(t *testing.T) {
	cases := []struct {
		name string
		mode os.FileMode
	}{
		{"world writable", 0777},
		{"group writable", 0770},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			stateRoot := filepath.Join(tempDir, "test_root")
			_ = os.MkdirAll(stateRoot, tc.mode)
			_ = os.Chmod(stateRoot, tc.mode)

			sd, err := statedir.Resolve(stateRoot)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if err := sd.EnsureDirs(); err != nil {
				t.Fatalf("EnsureDirs failed: %v", err)
			}

			fi, err := os.Stat(stateRoot)
			if err != nil {
				t.Fatalf("failed to stat stateRoot: %v", err)
			}
			if fi.Mode().Perm() != statedir.DirPerm {
				t.Errorf("expected EnsureDirs to enforce %04o for %s, got %04o", statedir.DirPerm, tc.name, fi.Mode().Perm())
			}
		})
	}
}

func TestStateDir_ParentSymlinkComponent(t *testing.T) {
	tempDir := t.TempDir()
	realParent := filepath.Join(tempDir, "real_parent")
	_ = os.MkdirAll(realParent, 0700)

	symlinkParent := filepath.Join(tempDir, "symlink_parent")
	if err := os.Symlink(realParent, symlinkParent); err != nil {
		t.Fatalf("failed to create symlink parent: %v", err)
	}

	stateRoot := filepath.Join(symlinkParent, "nested", "state")
	sd, err := statedir.Resolve(stateRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := sd.EnsureDirs(); err == nil {
		t.Fatalf("expected EnsureDirs to fail when parent path component is a symlink")
	}
}

func TestStateDir_WSL_And_Defaults(t *testing.T) {
	// WSL env flags
	t.Setenv("WSL_DISTRO_NAME", "Ubuntu")
	t.Setenv("WSL_INTEROP", "/run/WSL/1_interop")
	t.Setenv(statedir.EnvStateDir, "")

	sd, err := statedir.Resolve("")
	if err == nil {
		if sd.Root() == "" {
			t.Errorf("expected non-empty root for WSL")
		}
	}
}

func TestStateDir_NonWSL_POSIX_Defaults(t *testing.T) {
	t.Setenv(statedir.EnvStateDir, "")
	t.Setenv("AMC_TEST_NON_WSL", "1")

	// 1. With XDG_STATE_HOME
	tempDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tempDir)
	sd, err := statedir.Resolve("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := filepath.Join(tempDir, "amc")
	if sd.Root() != expected {
		t.Errorf("expected XDG root %q, got %q", expected, sd.Root())
	}

	// 2. Without XDG_STATE_HOME (fallback to ~/.local/state/amc)
	t.Setenv("XDG_STATE_HOME", "")
	sd, err = statedir.Resolve("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(sd.Root(), filepath.Join(".local", "state", "amc")) {
		t.Errorf("expected ~/.local/state/amc suffix, got %q", sd.Root())
	}
}

func TestStateDir_WSL_ResolutionFailure(t *testing.T) {
	t.Setenv(statedir.EnvStateDir, "")
	t.Setenv("WSL_DISTRO_NAME", "Ubuntu")
	t.Setenv("AMC_TEST_WSL_FAIL", "1")

	_, err := statedir.Resolve("")
	if err == nil || !errors.Is(err, statedir.ErrWSLHostPathResolution) {
		t.Fatalf("expected ErrWSLHostPathResolution, got %v", err)
	}
}

func TestStateDir_Windows_Default(t *testing.T) {
	t.Setenv(statedir.EnvStateDir, "")
	t.Setenv("AMC_TEST_WINDOWS", "1")

	t.Setenv("ProgramData", `C:\ProgramData`)
	sd, err := statedir.Resolve("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sd.Root() == "" {
		t.Errorf("expected non-empty windows root")
	}

	t.Setenv("ProgramData", "")
	t.Setenv("ALLUSERSPROFILE", `C:\ProgramData`)
	_, _ = statedir.Resolve("")

	t.Setenv("ALLUSERSPROFILE", "")
	_, _ = statedir.Resolve("")
}

func TestStateDir_NewSubdirs(t *testing.T) {
	tempDir := t.TempDir()
	sd, err := statedir.Resolve(tempDir)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if sd.DaemonDir() != filepath.Join(tempDir, statedir.SubdirDaemon) {
		t.Errorf("unexpected DaemonDir: %s", sd.DaemonDir())
	}
	if sd.AuthDir() != filepath.Join(tempDir, statedir.SubdirAuth) {
		t.Errorf("unexpected AuthDir: %s", sd.AuthDir())
	}
	if sd.OperationsDir() != filepath.Join(tempDir, statedir.SubdirOperations) {
		t.Errorf("unexpected OperationsDir: %s", sd.OperationsDir())
	}

	if err := sd.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs failed: %v", err)
	}
}
