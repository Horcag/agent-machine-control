//go:build windows

package target

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Horcag/agent-machine-control/internal/winacl"
	"golang.org/x/sys/windows"
)

type platformSecurity struct{}

func newPlatformSecurity() Security { return &platformSecurity{} }

func (*platformSecurity) setWindowsGuard(WindowsPathGuard) {}

func (*platformSecurity) setHostPathDetector(HostPathDetector) {}

func (*platformSecurity) ValidateDir(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateWindowsComponents(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("target: protected directory has unexpected type")
	}
	return winacl.ValidatePrivateFile(path)
}

func (*platformSecurity) ValidateFile(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateWindowsComponents(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("target: protected file has unexpected type")
	}
	return winacl.ValidatePrivateFile(path)
}

func (*platformSecurity) ProtectFile(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return winacl.ProtectPrivateFile(path)
}

func validateWindowsComponents(path string) error {
	cleaned := filepath.Clean(path)
	volume := filepath.VolumeName(cleaned)
	current := volume + string(filepath.Separator)
	rest := strings.TrimPrefix(cleaned[len(volume):], string(filepath.Separator))
	for _, part := range strings.Split(rest, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		path16, err := windows.UTF16PtrFromString(current)
		if err != nil {
			return err
		}
		attributes, err := windows.GetFileAttributes(path16)
		if err != nil {
			return err
		}
		if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return fmt.Errorf("target: protected path component %q is a reparse point", current)
		}
	}
	return nil
}
