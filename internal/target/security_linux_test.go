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
	mu                     sync.Mutex
	validate               []PathKind
	protect                []PathKind
	protectNew             []PathKind
	privateDirectoryCreate []PathKind
	err                    error
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

func (g *recordingWindowsGuard) ProtectNew(_ context.Context, _ string, kind PathKind) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.protectNew = append(g.protectNew, kind)
	return g.err
}

func (g *recordingWindowsGuard) CreatePrivateDirectory(_ context.Context, path string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.privateDirectoryCreate = append(g.privateDirectoryCreate, PathDirectory)
	if g.err != nil {
		return false, g.err
	}
	if err := os.Mkdir(path, 0700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func TestHostBackedMutationJournalDirectoryUsesAtomicWindowsCreate(t *testing.T) {
	t.Run("fresh directory", func(t *testing.T) {
		guard := &recordingWindowsGuard{}
		security := &platformSecurity{
			detectHostPath: func(string) (bool, error) { return true, nil },
			windowsGuard:   guard,
		}
		if _, err := NewMutationJournal(t.TempDir(), WithMutationJournalSecurity(security)); err != nil {
			t.Fatal(err)
		}
		guard.mu.Lock()
		defer guard.mu.Unlock()
		if got, want := guard.privateDirectoryCreate, []PathKind{PathDirectory}; !slices.Equal(got, want) {
			t.Fatalf("atomic directory creates = %v, want %v", got, want)
		}
		if len(guard.protectNew) != 0 || len(guard.protect) != 0 {
			t.Fatalf("host-backed mutation directory used post-create protection: protect=%v protect-new=%v", guard.protect, guard.protectNew)
		}
		if got, want := guard.validate, []PathKind{PathDirectory}; !slices.Equal(got, want) {
			t.Fatalf("validation calls = %v, want %v", got, want)
		}
	})

	t.Run("existing directory is validation only", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, mutationDirName), 0700); err != nil {
			t.Fatal(err)
		}
		guard := &recordingWindowsGuard{}
		security := &platformSecurity{
			detectHostPath: func(string) (bool, error) { return true, nil },
			windowsGuard:   guard,
		}
		if _, err := NewMutationJournal(root, WithMutationJournalSecurity(security)); err != nil {
			t.Fatal(err)
		}
		guard.mu.Lock()
		defer guard.mu.Unlock()
		if got, want := guard.privateDirectoryCreate, []PathKind{PathDirectory}; !slices.Equal(got, want) {
			t.Fatalf("atomic directory creates = %v, want %v", got, want)
		}
		if len(guard.protectNew) != 0 || len(guard.protect) != 0 {
			t.Fatalf("existing host-backed mutation directory was normalized: protect=%v protect-new=%v", guard.protect, guard.protectNew)
		}
		if got, want := guard.validate, []PathKind{PathDirectory}; !slices.Equal(got, want) {
			t.Fatalf("validation calls = %v, want %v", got, want)
		}
	})
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
	if !slices.Contains(guard.protect, PathDirectory) || !slices.Contains(guard.protectNew, PathFile) ||
		!slices.Contains(guard.validate, PathInheritedFile) || len(guard.validate) < 3 {
		t.Fatalf("guard calls = validate %v protect %v protect-new %v", guard.validate, guard.protect, guard.protectNew)
	}
}

func TestHostBackedFreshProtectionUsesDistinctGuardAction(t *testing.T) {
	guard := &recordingWindowsGuard{}
	security := &platformSecurity{
		detectHostPath: func(string) (bool, error) { return true, nil },
		windowsGuard:   guard,
	}
	if err := security.ProtectNewDir(context.Background(), t.TempDir()); err != nil {
		t.Fatalf("ProtectNewDir: %v", err)
	}
	path := filepath.Join(t.TempDir(), "fresh-file")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := security.ProtectNewFile(context.Background(), path); err != nil {
		t.Fatalf("ProtectNewFile: %v", err)
	}
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if !slices.Contains(guard.protectNew, PathDirectory) || !slices.Contains(guard.protectNew, PathFile) {
		t.Fatalf("fresh guard calls = %v, want directory and file", guard.protectNew)
	}
	if len(guard.protect) != 0 {
		t.Fatalf("ordinary guard protection used for fresh objects: %v", guard.protect)
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
	if !strings.Contains(windowsGuardScript, "$action -eq 'protect' -and $initialOwner -ne $identity.User.Value") {
		t.Fatal("PowerShell guard does not keep existing-object owner rejection distinct from fresh protection")
	}
	if !strings.Contains(windowsGuardScript, "create-private-directory") || !strings.Contains(windowsGuardScript, "CreateDirectoryW") {
		t.Fatal("PowerShell guard does not expose the atomic private-directory action")
	}
	if strings.Contains(windowsGuardScript, "AMC_TARGET_GUARD_") || !strings.Contains(windowsGuardScript, "[Console]::In.ReadToEnd()") {
		t.Fatal("PowerShell guard does not receive its request exclusively over standard input")
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

func TestWindowsGuardScriptUsesPowerShell51CompatibleDirectoryParent(t *testing.T) {
	const parentPathPrimitive = "$parentPath = [IO.Path]::GetDirectoryName($path)"
	if !strings.Contains(windowsGuardScript, parentPathPrimitive) {
		t.Fatalf("PowerShell guard does not derive the directory parent with %q", parentPathPrimitive)
	}
	if strings.Contains(windowsGuardScript, "Split-Path -LiteralPath $path -Parent") {
		t.Fatal("PowerShell guard retains the Windows PowerShell 5.1-incompatible parent derivation")
	}
}

func TestWindowsGuardScriptUsesCSharpSecurityAttributesSize(t *testing.T) {
	const sizeHelper = "public static uint SecurityAttributesSize() {\n    return (uint)Marshal.SizeOf(typeof(SECURITY_ATTRIBUTES));\n  }"
	if !strings.Contains(windowsGuardScript, sizeHelper) {
		t.Fatal("PowerShell guard does not calculate SECURITY_ATTRIBUTES size in its C# helper")
	}
	if !strings.Contains(windowsGuardScript, "$attributes.nLength = [AmcNativeDirectory]::SecurityAttributesSize()") {
		t.Fatal("PowerShell guard does not use the C# SECURITY_ATTRIBUTES size helper")
	}
	if strings.Contains(windowsGuardScript, "[Runtime.InteropServices.Marshal]::SizeOf(") {
		t.Fatal("PowerShell guard retains the PowerShell 5.1-incompatible Marshal.SizeOf invocation")
	}
}

func TestPowerShellWindowsGuardCommandProofs(t *testing.T) {
	commandDir := t.TempDir()
	writeGuardExecutable(t, commandDir, "wslpath", "#!/bin/sh\nprintf 'C:\\\\fake\\\\target\\n'\n")
	t.Setenv("PATH", commandDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	guard := powerShellWindowsGuard{}

	t.Run("valid proof", func(t *testing.T) {
		writeGuardExecutable(t, commandDir, "powershell.exe", `#!/bin/sh
request=$(cat)
case "$request" in
  *'"kind":"directory"'*) kind=directory; flags=3; protected=true ;;
  *'"kind":"file"'*) kind=file; flags=0; protected=true ;;
  *'"kind":"inherited_file"'*) kind=inherited_file; flags=16; protected=false ;;
  *) exit 1 ;;
esac
printf '{"owner":"S-1-5-21-1000","current_user":"S-1-5-21-1000","protected":%s,"kind":"%s","entries":[{"type":0,"flags":%s,"mask":2032127,"sid":"S-1-5-21-1000"},{"type":0,"flags":%s,"mask":2032127,"sid":"S-1-5-18"},{"type":0,"flags":%s,"mask":2032127,"sid":"S-1-5-32-544"}]}\n' "$protected" "$kind" "$flags" "$flags" "$flags"
`)
		if err := guard.Validate(context.Background(), "/mnt/c/fake", PathDirectory); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if err := guard.ProtectNew(context.Background(), "/mnt/c/fake", PathFile); err != nil {
			t.Fatalf("ProtectNew: %v", err)
		}
		created, err := guard.CreatePrivateDirectory(context.Background(), "/mnt/c/fake")
		if err != nil || created {
			t.Fatalf("CreatePrivateDirectory = %t, %v; want existing valid proof", created, err)
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

func TestPowerShellWindowsGuardRequestTransportFailsClosed(t *testing.T) {
	commandDir := t.TempDir()
	writeGuardExecutable(t, commandDir, "wslpath", "#!/bin/sh\nprintf 'C:\\\\fake\\\\target\\n'\n")
	writeGuardExecutable(t, commandDir, "powershell.exe", `#!/bin/sh
request=$(cat)
expected='{"path":"C:\\fake\\target","kind":"directory","action":"validate"}'
[ "$request" = "$expected" ] || exit 1
printf '{"owner":"S-1-5-21-1000","current_user":"S-1-5-21-1000","protected":true,"kind":"directory","entries":[{"type":0,"flags":3,"mask":2032127,"sid":"S-1-5-21-1000"},{"type":0,"flags":3,"mask":2032127,"sid":"S-1-5-18"},{"type":0,"flags":3,"mask":2032127,"sid":"S-1-5-32-544"}]}'
`)
	t.Setenv("PATH", commandDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := (powerShellWindowsGuard{}).Validate(context.Background(), "/mnt/c/fake", PathDirectory); err != nil {
		t.Fatalf("Validate exact JSON request: %v", err)
	}
	for _, request := range [][]byte{nil, []byte(`{"path":`), []byte(`{"path":"C:\\fake\\target","kind":"directory","action":"validate"}{}`)} {
		if _, err := runWindowsGuardRequest(context.Background(), request, PathDirectory); err == nil {
			t.Fatalf("runWindowsGuardRequest(%q) unexpectedly accepted malformed input", request)
		}
	}
}

func TestPowerShellWindowsGuardSeparatesExistingAndFreshProtection(t *testing.T) {
	commandDir := t.TempDir()
	writeGuardExecutable(t, commandDir, "wslpath", "#!/bin/sh\nprintf 'C:\\\\fake\\\\target\\n'\n")
	writeGuardExecutable(t, commandDir, "powershell.exe", `#!/bin/sh
request=$(cat)
case "$request" in
  *'"action":"protect"'*) exit 1 ;;
  *'"action":"protect-new"'*) printf '{"owner":"S-1-5-21-1000","current_user":"S-1-5-21-1000","protected":true,"kind":"file","entries":[{"type":0,"flags":0,"mask":2032127,"sid":"S-1-5-21-1000"},{"type":0,"flags":0,"mask":2032127,"sid":"S-1-5-18"},{"type":0,"flags":0,"mask":2032127,"sid":"S-1-5-32-544"}]}' ;;
  *) exit 1 ;;
esac
`)
	t.Setenv("PATH", commandDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	guard := powerShellWindowsGuard{}
	if err := guard.Protect(context.Background(), "/mnt/c/fake", PathFile); err == nil {
		t.Fatal("Protect unexpectedly accepted a foreign-owner proof")
	}
	if err := guard.ProtectNew(context.Background(), "/mnt/c/fake", PathFile); err != nil {
		t.Fatalf("ProtectNew = %v", err)
	}
}

func TestPowerShellWindowsGuardPrivateDirectoryActionIsDistinctAndRequiresProof(t *testing.T) {
	commandDir := t.TempDir()
	writeGuardExecutable(t, commandDir, "wslpath", "#!/bin/sh\nprintf 'C:\\\\fake\\\\target\\n'\n")
	writeGuardExecutable(t, commandDir, "powershell.exe", `#!/bin/sh
request=$(cat)
case "$request" in
  *'"action":"create-private-directory"'*) printf '{"owner":"S-1-5-21-1000","current_user":"S-1-5-21-1000","protected":true,"kind":"directory","created":true,"entries":[{"type":0,"flags":3,"mask":2032127,"sid":"S-1-5-21-1000"},{"type":0,"flags":3,"mask":2032127,"sid":"S-1-5-18"},{"type":0,"flags":3,"mask":2032127,"sid":"S-1-5-32-544"}]}' ;;
  *) exit 1 ;;
esac
`)
	t.Setenv("PATH", commandDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	guard := powerShellWindowsGuard{}
	created, err := guard.CreatePrivateDirectory(context.Background(), "/mnt/c/fake")
	if err != nil || !created {
		t.Fatalf("CreatePrivateDirectory = %t, %v; want atomically created valid directory", created, err)
	}

	writeGuardExecutable(t, commandDir, "powershell.exe", "#!/bin/sh\nprintf '{}\\n'\n")
	if _, err := guard.CreatePrivateDirectory(context.Background(), "/mnt/c/fake"); err == nil {
		t.Fatal("CreatePrivateDirectory accepted an invalid proof")
	}
}

func TestPowerShellWindowsGuardLocalSystemProofs(t *testing.T) {
	commandDir := t.TempDir()
	writeGuardExecutable(t, commandDir, "wslpath", "#!/bin/sh\nprintf 'C:\\\\fake\\\\target\\n'\n")
	writeGuardExecutable(t, commandDir, "powershell.exe", `#!/bin/sh
request=$(cat)
case "$request" in
  *'"kind":"directory"'*) kind=directory; flags=3; protected=true ;;
  *'"kind":"file"'*) kind=file; flags=0; protected=true ;;
  *'"kind":"inherited_file"'*) kind=inherited_file; flags=16; protected=false ;;
  *) exit 1 ;;
esac
printf '{"owner":"S-1-5-18","current_user":"S-1-5-18","protected":%s,"kind":"%s","entries":[{"type":0,"flags":%s,"mask":2032127,"sid":"S-1-5-18"},{"type":0,"flags":%s,"mask":2032127,"sid":"S-1-5-32-544"}]}\n' "$protected" "$kind" "$flags" "$flags"
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
			err = guard.ProtectNew(context.Background(), "/mnt/c/fake", check.kind)
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
