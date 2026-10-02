package bootstrap

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
)

func TestLauncherArtifactRejectsMissingAndInvalidCompanions(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	binary := filepath.Join(directory, "amcd")
	if _, _, err := launcherArtifact(binary, "Synthetic-WSL"); !errors.Is(err, app.ErrBootstrapUnsupported) {
		t.Fatalf("missing companion accepted: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "amcd-launcher.exe"), []byte("not a PE file"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := launcherArtifact(binary, "Synthetic-WSL"); !errors.Is(err, app.ErrBootstrapUnsupported) {
		t.Fatalf("invalid companion accepted: %v", err)
	}
}

func TestLauncherArtifactRejectsConsoleSubsystem(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	writeTestLauncher(t, directory)
	file, err := os.OpenFile(filepath.Join(directory, "amcd-launcher.exe"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteAt([]byte{3, 0}, 128+24+68); err != nil {
		t.Fatal(err)
	}
	if _, _, err := launcherArtifact(filepath.Join(directory, "amcd"), "Synthetic-WSL"); !errors.Is(err, app.ErrBootstrapUnsupported) {
		t.Fatalf("console companion accepted: %v", err)
	}
}

func TestLauncherArtifactRejectsSymlink(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	if err := os.Symlink(filepath.Join(directory, "absent.exe"), filepath.Join(directory, "amcd-launcher.exe")); err != nil {
		t.Skip("symlink creation unavailable")
	}
	if _, _, err := launcherArtifact(filepath.Join(directory, "amcd"), "Synthetic-WSL"); !errors.Is(err, app.ErrBootstrapUnsupported) {
		t.Fatalf("symlink companion accepted: %v", err)
	}
}

func TestLauncherArtifactPinsWindowsLocalhostSourceAndHash(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	writeTestLauncher(t, directory)
	source, digest, err := launcherArtifact(filepath.Join(directory, "amcd"), "Synthetic-WSL")
	if err != nil {
		t.Fatal(err)
	}
	want, err := hashRegularFile(filepath.Join(directory, "amcd-launcher.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if digest != want {
		t.Fatal("launcher fingerprint does not match source")
	}
	if source[:len(`\\wsl.localhost\Synthetic-WSL`)] != `\\wsl.localhost\Synthetic-WSL` {
		t.Fatalf("unexpected source %q", source)
	}
}
