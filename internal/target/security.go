package target

import "context"

// PathKind identifies the protected object expected by a Windows host-path guard.
type PathKind string

const (
	PathDirectory     PathKind = "directory"
	PathFile          PathKind = "file"
	PathInheritedFile PathKind = "inherited_file"
)

// WindowsPathGuard proves Windows owner, DACL, and reparse invariants for host-backed paths.
type WindowsPathGuard interface {
	Validate(context.Context, string, PathKind) error
	Protect(context.Context, string, PathKind) error
	ProtectNew(context.Context, string, PathKind) error
}

// HostPathDetector distinguishes native POSIX state from Windows-host-backed state.
type HostPathDetector func(string) (bool, error)

// Security validates the protected directory and canonical or temporary files.
type Security interface {
	ValidateDir(context.Context, string) error
	ProtectDir(context.Context, string) error
	ProtectNewDir(context.Context, string) error
	ValidateInheritedFile(context.Context, string) error
	ValidateFile(context.Context, string) error
	ProtectNewFile(context.Context, string) error
}

type configurableSecurity interface {
	Security
	setWindowsGuard(WindowsPathGuard)
	setHostPathDetector(HostPathDetector)
}

// WithWindowsPathGuard injects the bounded Windows security proof used for host-backed paths.
func WithWindowsPathGuard(guard WindowsPathGuard) Option {
	return func(store *Store) {
		if configurable, ok := store.security.(configurableSecurity); ok {
			configurable.setWindowsGuard(guard)
		}
	}
}

// WithHostPathDetector injects host-path classification for focused tests.
func WithHostPathDetector(detector HostPathDetector) Option {
	return func(store *Store) {
		if configurable, ok := store.security.(configurableSecurity); ok {
			configurable.setHostPathDetector(detector)
		}
	}
}
