// Package winlauncher starts the Windows bootstrap wrapper without creating a console.
package winlauncher

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Run accepts only the bootstrap's fixed PowerShell invocation and waits for its exit.
// A child exit is returned unchanged; validation and launch failures return code 1.
func Run(args []string) (int, error) {
	if err := validateArgs(args); err != nil {
		return 1, err
	}
	return launch(args)
}

func validateArgs(args []string) error {
	want := [...]string{"-NoLogo", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-ExecutionPolicy", "Bypass", "-File"}
	if len(args) != len(want)+1 {
		return errors.New("launcher requires the fixed bootstrap PowerShell arguments")
	}
	for i, value := range want {
		if args[i] != value {
			return fmt.Errorf("unexpected bootstrap argument at position %d", i+1)
		}
	}
	if !filepath.IsAbs(args[len(want)]) || strings.ContainsRune(args[len(want)], 0) {
		return errors.New("bootstrap wrapper path must be absolute and contain no NUL")
	}
	return nil
}

func commandResult(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
		return exitErr.ExitCode(), nil
	}
	return 1, fmt.Errorf("run bootstrap PowerShell: %w", err)
}
