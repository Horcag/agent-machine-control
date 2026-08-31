//go:build linux

package target

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordingWindowsGuard struct {
	mu       sync.Mutex
	validate []PathKind
	protect  []PathKind
	err      error
}

func (g *recordingWindowsGuard) Validate(_ context.Context, _ string, kind PathKind) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.validate = append(g.validate, kind)
	return g.err
}

func (g *recordingWindowsGuard) Protect(_ context.Context, _ string, kind PathKind) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.protect = append(g.protect, kind)
	return g.err
}

func TestHostBackedPathUsesInjectedWindowsGuard(t *testing.T) {
	dir := testDirectory(t)
	guard := &recordingWindowsGuard{}
	store := testStore(t, dir,
		WithHostPathDetector(func(string) (bool, error) { return true, nil }),
		WithWindowsPathGuard(guard),
	)
	if _, err := store.Save(context.Background(), testDefault(t, vmA)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if !slices.Contains(guard.protect, PathDirectory) || !slices.Contains(guard.protect, PathFile) ||
		!slices.Contains(guard.validate, PathInheritedFile) || len(guard.validate) < 3 {
		t.Fatalf("guard calls = validate %v protect %v", guard.validate, guard.protect)
	}
}

func TestHostBackedPathFailsClosedWithoutSecurityProof(t *testing.T) {
	dir := testDirectory(t)
	store := testStore(t, dir,
		WithHostPathDetector(func(string) (bool, error) { return true, nil }),
		WithWindowsPathGuard(nil),
	)
	if _, err := store.Load(context.Background()); !errors.Is(err, ErrHostSecurityUnproven) {
		t.Fatalf("Load error = %v, want ErrHostSecurityUnproven", err)
	}
}

func TestHostBackedSaveFailsClosedWithoutDirectoryProtectionGuard(t *testing.T) {
	dir := testDirectory(t)
	store := testStore(t, dir,
		WithHostPathDetector(func(string) (bool, error) { return true, nil }),
		WithWindowsPathGuard(nil),
	)
	publication, err := store.Save(context.Background(), testDefault(t, vmA))
	if !errors.Is(err, ErrHostSecurityUnproven) || publication.Committed {
		t.Fatalf("Save = %+v, %v, want pre-commit host security failure", publication, err)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("target entries = %v, want no state created", entries)
	}
}

func TestHostBackedSaveRejectsSymlinkComponentBeforeCreatingTemporaryState(t *testing.T) {
	realDir := testDirectory(t)
	linkedDir := filepath.Join(t.TempDir(), "linked-targets")
	if err := os.Symlink(realDir, linkedDir); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	guard := &recordingWindowsGuard{}
	store := testStore(t, linkedDir,
		WithHostPathDetector(func(string) (bool, error) { return true, nil }),
		WithWindowsPathGuard(guard),
	)
	publication, err := store.Save(context.Background(), testDefault(t, vmA))
	if err == nil || publication.Committed {
		t.Fatalf("Save = %+v, %v, want pre-commit symlink rejection", publication, err)
	}
	entries, readErr := os.ReadDir(realDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("target entries = %v, want no temporary or canonical state", entries)
	}
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if len(guard.protect) != 0 {
		t.Fatalf("Windows guard mutated after local symlink rejection: %v", guard.protect)
	}
}

func TestHostPathDetectorRecognizesDriveMountShape(t *testing.T) {
	hostBacked, err := detectWindowsHostPath("/mnt/c/ProgramData/amc/targets")
	if err != nil || !hostBacked {
		t.Fatalf("detectWindowsHostPath = %t, %v", hostBacked, err)
	}
}

func TestWindowsGuardScriptParses(t *testing.T) {
	wantDistinctSet := "$allowed = @($identity.User.Value, 'S-1-5-18', 'S-1-5-32-544') | Select-Object -Unique"
	if !strings.Contains(windowsGuardScript, wantDistinctSet) {
		t.Fatal("PowerShell guard does not preserve the ordered distinct trustee set")
	}
	powerShell, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("powershell.exe unavailable")
	}
	command := exec.Command(powerShell, "-NoProfile", "-NonInteractive", "-Command", "[scriptblock]::Create([Console]::In.ReadToEnd()) | Out-Null")
	command.Stdin = strings.NewReader(windowsGuardScript)
	command.Env = os.Environ()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("PowerShell parser rejected guard: %v: %s", err, output)
	}
}

func TestPowerShellWindowsGuardCommandProofs(t *testing.T) {
	commandDir := t.TempDir()
	writeGuardExecutable(t, commandDir, "wslpath", "#!/bin/sh\nprintf 'C:\\\\fake\\\\target\\n'\n")
	t.Setenv("PATH", commandDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	guard := powerShellWindowsGuard{}

	t.Run("valid proof", func(t *testing.T) {
		writeGuardExecutable(t, commandDir, "powershell.exe", `#!/bin/sh
case "$AMC_TARGET_GUARD_KIND" in
  directory) flags=3; protected=true ;;
  file) flags=0; protected=true ;;
  inherited_file) flags=16; protected=false ;;
esac
printf '{"owner":"S-1-5-21-1000","current_user":"S-1-5-21-1000","protected":%s,"kind":"%s","entries":[{"type":0,"flags":%s,"mask":2032127,"sid":"S-1-5-21-1000"},{"type":0,"flags":%s,"mask":2032127,"sid":"S-1-5-18"},{"type":0,"flags":%s,"mask":2032127,"sid":"S-1-5-32-544"}]}\n' "$protected" "$AMC_TARGET_GUARD_KIND" "$flags" "$flags" "$flags"
`)
		if err := guard.Validate(context.Background(), "/mnt/c/fake", PathDirectory); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if err := guard.Protect(context.Background(), "/mnt/c/fake", PathFile); err != nil {
			t.Fatalf("Protect: %v", err)
		}
	})

	t.Run("invalid proof", func(t *testing.T) {
		writeGuardExecutable(t, commandDir, "powershell.exe", "#!/bin/sh\nprintf '{}\\n'\n")
		if err := guard.Validate(context.Background(), "/mnt/c/fake", PathFile); err == nil {
			t.Fatal("invalid proof unexpectedly accepted")
		}
	})

	t.Run("PowerShell failure", func(t *testing.T) {
		writeGuardExecutable(t, commandDir, "powershell.exe", "#!/bin/sh\nexit 1\n")
		if err := guard.Validate(context.Background(), "/mnt/c/fake", PathFile); err == nil {
			t.Fatal("PowerShell failure unexpectedly accepted")
		}
	})

	t.Run("oversized proof", func(t *testing.T) {
		body := "#!/bin/sh\nprintf '" + strings.Repeat("x", maxGuardOutputBytes+1) + "'\n"
		writeGuardExecutable(t, commandDir, "powershell.exe", body)
		if err := guard.Validate(context.Background(), "/mnt/c/fake", PathFile); err == nil {
			t.Fatal("oversized proof unexpectedly accepted")
		}
	})

	t.Run("path conversion failure", func(t *testing.T) {
		writeGuardExecutable(t, commandDir, "wslpath", "#!/bin/sh\nexit 1\n")
		if err := guard.Validate(context.Background(), "/mnt/c/fake", PathFile); err == nil {
			t.Fatal("conversion failure unexpectedly accepted")
		}
	})
}

func TestPowerShellWindowsGuardLocalSystemProofs(t *testing.T) {
	commandDir := t.TempDir()
	writeGuardExecutable(t, commandDir, "wslpath", "#!/bin/sh\nprintf 'C:\\\\fake\\\\target\\n'\n")
	writeGuardExecutable(t, commandDir, "powershell.exe", `#!/bin/sh
case "$AMC_TARGET_GUARD_KIND:$AMC_TARGET_GUARD_ACTION" in
  directory:validate|directory:protect) flags=3; protected=true ;;
  file:validate|file:protect) flags=0; protected=true ;;
  inherited_file:validate) flags=16; protected=false ;;
  *) exit 1 ;;
esac
printf '{"owner":"S-1-5-18","current_user":"S-1-5-18","protected":%s,"kind":"%s","entries":[{"type":0,"flags":%s,"mask":2032127,"sid":"S-1-5-18"},{"type":0,"flags":%s,"mask":2032127,"sid":"S-1-5-32-544"}]}\n' "$protected" "$AMC_TARGET_GUARD_KIND" "$flags" "$flags"
`)
	t.Setenv("PATH", commandDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	guard := powerShellWindowsGuard{}
	checks := []struct {
		kind    PathKind
		protect bool
	}{
		{kind: PathDirectory, protect: true},
		{kind: PathFile, protect: true},
		{kind: PathInheritedFile},
	}
	for _, check := range checks {
		var err error
		if check.protect {
			err = guard.Protect(context.Background(), "/mnt/c/fake", check.kind)
		} else {
			err = guard.Validate(context.Background(), "/mnt/c/fake", check.kind)
		}
		if err != nil {
			t.Fatalf("LocalSystem %s proof: %v", check.kind, err)
		}
	}
}

func TestBoundedGuardContextHonorsShorterCallerDeadline(t *testing.T) {
	short, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bounded, boundedCancel := boundedGuardContext(short)
	defer boundedCancel()
	deadline, ok := bounded.Deadline()
	if !ok || time.Until(deadline) > 2*time.Second {
		t.Fatalf("bounded deadline = %v, %t", deadline, ok)
	}
	defaultBounded, defaultCancel := boundedGuardContext(context.Background())
	defer defaultCancel()
	deadline, ok = defaultBounded.Deadline()
	if !ok || time.Until(deadline) > 5*time.Second {
		t.Fatalf("default deadline = %v, %t", deadline, ok)
	}
}

func TestOpenNoFollowAndAtomicReplaceFailurePaths(t *testing.T) {
	dir := testDirectory(t)
	if _, err := openNoFollow(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("missing no-follow open unexpectedly succeeded")
	}
	if _, err := openNoFollow(dir); err == nil {
		t.Fatal("directory no-follow open unexpectedly succeeded")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if result := atomicReplace(canceled, "old", "new"); !errors.Is(result.Err, context.Canceled) || result.Committed {
		t.Fatalf("canceled atomicReplace = %+v", result)
	}
	if result := atomicReplace(context.Background(), filepath.Join(dir, "missing"), filepath.Join(dir, "new")); result.Err == nil || result.Committed {
		t.Fatalf("missing atomicReplace = %+v", result)
	}
}

func writeGuardExecutable(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatalf("WriteFile %s: %v", name, err)
	}
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatalf("Chmod %s: %v", name, err)
	}
}
