//go:build unix

package target

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type platformSecurity struct {
	detectHostPath HostPathDetector
	windowsGuard   WindowsPathGuard
}

func newPlatformSecurity() Security {
	return &platformSecurity{
		detectHostPath: detectWindowsHostPath,
		windowsGuard:   newPowerShellWindowsGuard(),
	}
}

func (s *platformSecurity) setWindowsGuard(guard WindowsPathGuard) {
	s.windowsGuard = guard
}

func (s *platformSecurity) setHostPathDetector(detector HostPathDetector) {
	s.detectHostPath = detector
}

func (s *platformSecurity) ValidateDir(ctx context.Context, path string) error {
	if err := validateNoSymlinkComponents(path); err != nil {
		return err
	}
	hostBacked, err := s.isHostBacked(path)
	if err != nil {
		return err
	}
	if hostBacked {
		return s.validateHostPath(ctx, path, PathDirectory)
	}
	return validatePOSIX(path, true, 0700)
}

func (s *platformSecurity) ProtectDir(ctx context.Context, path string) error {
	if err := validateNoSymlinkComponents(path); err != nil {
		return err
	}
	hostBacked, err := s.isHostBacked(path)
	if err != nil {
		return err
	}
	if hostBacked {
		if s.windowsGuard == nil {
			return ErrHostSecurityUnproven
		}
		return s.windowsGuard.Protect(ctx, path, PathDirectory)
	}
	if err := validatePOSIXOwnerAndType(path, true); err != nil {
		return err
	}
	if err := os.Chmod(path, 0700); err != nil {
		return err
	}
	return validatePOSIX(path, true, 0700)
}

func (s *platformSecurity) ValidateInheritedFile(ctx context.Context, path string) error {
	if err := validateNoSymlinkComponents(path); err != nil {
		return err
	}
	hostBacked, err := s.isHostBacked(path)
	if err != nil {
		return err
	}
	if hostBacked {
		return s.validateHostPath(ctx, path, PathInheritedFile)
	}
	return validatePOSIX(path, false, 0600)
}

func (s *platformSecurity) ValidateFile(ctx context.Context, path string) error {
	if err := validateNoSymlinkComponents(path); err != nil {
		return err
	}
	hostBacked, err := s.isHostBacked(path)
	if err != nil {
		return err
	}
	if hostBacked {
		return s.validateHostPath(ctx, path, PathFile)
	}
	return validatePOSIX(path, false, 0600)
}

func (s *platformSecurity) ProtectFile(ctx context.Context, path string) error {
	hostBacked, err := s.isHostBacked(path)
	if err != nil {
		return err
	}
	if !hostBacked {
		return os.Chmod(path, 0600)
	}
	if s.windowsGuard == nil {
		return ErrHostSecurityUnproven
	}
	return s.windowsGuard.Protect(ctx, path, PathFile)
}

func (s *platformSecurity) isHostBacked(path string) (bool, error) {
	if s.detectHostPath == nil {
		return false, ErrHostSecurityUnproven
	}
	return s.detectHostPath(path)
}

func (s *platformSecurity) validateHostPath(ctx context.Context, path string, kind PathKind) error {
	if s.windowsGuard == nil {
		return ErrHostSecurityUnproven
	}
	if err := s.windowsGuard.Validate(ctx, path, kind); err != nil {
		return errors.Join(ErrHostSecurityUnproven, err)
	}
	return nil
}

func validatePOSIX(path string, wantDir bool, mode os.FileMode) error {
	if err := validatePOSIXOwnerAndType(path, wantDir); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != mode {
		return fmt.Errorf("protected path %q mode is %04o, want %04o", path, info.Mode().Perm(), mode)
	}
	return nil
}

func validatePOSIXOwnerAndType(path string, wantDir bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || wantDir != info.IsDir() || !wantDir && !info.Mode().IsRegular() {
		return fmt.Errorf("protected path %q has unexpected type", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	effectiveUID, parseErr := strconv.ParseUint(strconv.Itoa(os.Geteuid()), 10, 32)
	if !ok || parseErr != nil || uint64(stat.Uid) != effectiveUID {
		return fmt.Errorf("protected path %q is not owned by the current user", path)
	}
	return nil
}

func validateNoSymlinkComponents(path string) error {
	return validateNoSymlinkComponentsWith(path, func(path string) (os.FileMode, error) {
		info, err := os.Lstat(path)
		if err != nil {
			return 0, err
		}
		return info.Mode(), nil
	}, allowedSystemPathAlias)
}

type componentMode func(string) (os.FileMode, error)

type systemPathAlias func(string) (string, bool, error)

func validateNoSymlinkComponentsWith(path string, lstat componentMode, allowedAlias systemPathAlias) error {
	cleaned := filepath.Clean(path)
	current := string(filepath.Separator)
	for _, part := range splitPath(cleaned) {
		current = filepath.Join(current, part)
		mode, err := lstat(current)
		if err != nil {
			return err
		}
		if mode&os.ModeSymlink != 0 {
			canonical, allowed, aliasErr := allowedAlias(current)
			if aliasErr != nil {
				return aliasErr
			}
			if allowed {
				current = canonical
				continue
			}
			return fmt.Errorf("protected path component %q is a symlink", current)
		}
	}
	return nil
}

func splitPath(path string) []string {
	volume := filepath.VolumeName(path)
	rest := path[len(volume):]
	return strings.FieldsFunc(filepath.ToSlash(rest), func(r rune) bool { return r == '/' })
}
