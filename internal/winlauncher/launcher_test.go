package winlauncher

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func bootstrapArgs(path string) []string {
	return []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-ExecutionPolicy", "Bypass", "-File", path}
}

func TestValidateArgs(t *testing.T) {
	valid := bootstrapArgs(filepath.Join(t.TempDir(), "wrapper with spaces.ps1"))
	if err := validateArgs(valid); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]string{
		"missing":  valid[:8],
		"extra":    append(append([]string(nil), valid...), "-Command"),
		"relative": bootstrapArgs("wrapper.ps1"),
		"nul":      bootstrapArgs(valid[8] + "\x00"),
	}
	for i := range 8 {
		changed := append([]string(nil), valid...)
		changed[i] = "unexpected"
		cases[valid[i]] = changed
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			code, err := Run(args)
			if code != 1 || err == nil {
				t.Fatalf("Run rejected invocation = (%d, %v), want (1, error)", code, err)
			}
		})
	}
}

func TestCommandResult(t *testing.T) {
	if os.Getenv("AMC_LAUNCHER_EXIT_HELPER") == "1" {
		os.Exit(23)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestCommandResult$")
	cmd.Env = append(os.Environ(), "AMC_LAUNCHER_EXIT_HELPER=1")
	code, err := commandResult(cmd.Run())
	if code != 23 || err != nil {
		t.Fatalf("child exit = (%d, %v), want (23, nil)", code, err)
	}
	code, err = commandResult(nil)
	if code != 0 || err != nil {
		t.Fatalf("success = (%d, %v), want (0, nil)", code, err)
	}
	startErr := errors.New("synthetic start failure")
	code, err = commandResult(startErr)
	if code != 1 || !errors.Is(err, startErr) {
		t.Fatalf("start failure = (%d, %v), want (1, wrapped error)", code, err)
	}
}
