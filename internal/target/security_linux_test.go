//go:build linux

package target

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordingWindowsGuard struct {
	mu       sync.Mutex
	validate []PathKind
	protect  int
	err      error
}

func (g *recordingWindowsGuard) Validate(_ context.Context, _ string, kind PathKind) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.validate = append(g.validate, kind)
	return g.err
}

func (g *recordingWindowsGuard) ProtectFile(context.Context, string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.protect++
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
	if guard.protect == 0 || len(guard.validate) < 3 || guard.validate[0] != PathDirectory {
		t.Fatalf("guard calls = validate %v protect %d", guard.validate, guard.protect)
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

func TestHostPathDetectorRecognizesDriveMountShape(t *testing.T) {
	hostBacked, err := detectWindowsHostPath("/mnt/c/ProgramData/amc/targets")
	if err != nil || !hostBacked {
		t.Fatalf("detectWindowsHostPath = %t, %v", hostBacked, err)
	}
}

func TestWindowsGuardScriptParses(t *testing.T) {
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
		writeGuardExecutable(t, commandDir, "powershell.exe", "#!/bin/sh\nprintf '{\"ok\":true}\\n'\n")
		if err := guard.Validate(context.Background(), "/mnt/c/fake", PathDirectory); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if err := guard.ProtectFile(context.Background(), "/mnt/c/fake"); err != nil {
			t.Fatalf("ProtectFile: %v", err)
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
