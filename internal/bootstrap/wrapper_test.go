package bootstrap

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
)

// Exercise the generated launcher with a disposable console-subsystem child. It
// records its real console allocation and argv, then fails to verify exit propagation.
func TestWrapperStartsConsolelessChildAndPreservesExitAndArguments(t *testing.T) {
	_, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("Windows PowerShell is required")
	}
	winPath := func(path string) string {
		t.Helper()
		if runtime.GOOS == "windows" {
			return path
		}
		out, err := exec.Command("wslpath", "-w", path).Output()
		if err != nil {
			t.Skip("WSL path translation is required")
		}
		return strings.TrimSpace(string(out))
	}
	dir, binary, launcher := buildWindowsTestExecutables(t)
	output := filepath.Join(dir, "child observation.json")
	spec := app.BootstrapSpec{WSLExecutable: winPath(binary), Distro: "Synthetic WSL", LinuxUser: "operator", BinaryPath: "/synthetic path/amcd", StateDir: winPath(output), ListenAddress: "127.0.0.1:0"}
	wrapper, err := wrapperBytes(spec)
	if err != nil {
		t.Fatal(err)
	}
	wrapperPath := filepath.Join(dir, "bootstrap wrapper.ps1")
	if err := os.WriteFile(wrapperPath, wrapper, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := launcherArtifact(filepath.Join(dir, "amcd"), "Synthetic-WSL"); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), launcher, "-NoLogo", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-ExecutionPolicy", "Bypass", "-File", winPath(wrapperPath))
	out, err := command.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 17 {
		t.Fatalf("launcher exit: %v, output %s", err, out)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var observation struct {
		Console uintptr
		Args    []string
	}
	if err := json.Unmarshal(data, &observation); err != nil {
		t.Fatal(err)
	}
	if observation.Console != 0 {
		t.Fatal("launcher allocated a console for its child")
	}
	want := []string{"-d", spec.Distro, "--user", spec.LinuxUser, "--exec", spec.BinaryPath, "run", "--state-dir", spec.StateDir, "--listen", spec.ListenAddress, "--json"}
	if !reflect.DeepEqual(observation.Args, want) {
		t.Fatalf("child arguments: %q, want %q", observation.Args, want)
	}
}

func buildWindowsTestExecutables(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "child.go")
	const fixture = `package main
import ("encoding/json"; "os"; "syscall")
func main() {
 console, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
 for i, arg := range os.Args { if arg == "--state-dir" && i+1 < len(os.Args) {
  b, _ := json.Marshal(struct { Console uintptr; Args []string }{console, os.Args[1:]})
  if os.WriteFile(os.Args[i+1], b, 0600) != nil { os.Exit(19) }
  os.Exit(17)
 } }
 os.Exit(18)
}`
	if err := os.WriteFile(source, []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "child.exe")
	launcherPath := filepath.Join(dir, "amcd-launcher.exe")
	fixtureBuild := exec.CommandContext(t.Context(), "go", "build", "-o", binary, source)
	fixtureBuild.Env = append(os.Environ(), "GOOS=windows")
	if result, err := fixtureBuild.CombinedOutput(); err != nil {
		t.Fatalf("Windows fixture build: %v\n%s", err, result)
	}
	launcher := exec.CommandContext(t.Context(), "go", "build", "-ldflags=-H=windowsgui", "-o", launcherPath, "../../cmd/amcd-launcher")
	launcher.Env = append(os.Environ(), "GOOS=windows")
	if result, err := launcher.CombinedOutput(); err != nil {
		t.Fatalf("GUI launcher build: %v\n%s", err, result)
	}
	return dir, binary, launcherPath
}
