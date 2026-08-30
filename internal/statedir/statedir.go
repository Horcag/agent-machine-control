package statedir

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	// EnvStateDir is the environment variable that can override the default state directory.
	EnvStateDir = "AMC_STATE_DIR"

	// DirPerm is the restrictive directory permission for state directories (0700).
	DirPerm = 0700

	// SubdirLeases is the subdirectory name for per-machine leases.
	SubdirLeases = "leases"

	// SubdirReceipts is the subdirectory name for structured execution receipts.
	SubdirReceipts = "receipts"

	// SubdirAudit is the subdirectory name for append-only audit event logs.
	SubdirAudit = "audit"

	// SubdirApprovals is the subdirectory name for consumed approvals tracking.
	SubdirApprovals = "approvals"

	// SubdirDaemon is the subdirectory name for daemon runtime metadata.
	SubdirDaemon = "daemon"

	// SubdirAuth is the subdirectory name for bearer token authentication files.
	SubdirAuth = "auth"

	// SubdirOperations is the subdirectory name for durable operation records.
	SubdirOperations = "operations"

	// SubdirSessions is the subdirectory name for persistent terminal session state and transcripts.
	SubdirSessions = "sessions"

	// SubdirKeys is the subdirectory name for local guest private key files.
	SubdirKeys = "keys"

	// SubdirMachines is the subdirectory name for per-machine connection and host-key configurations.
	SubdirMachines = "machines"
)

var (
	// ErrSymlinkNotAllowed indicates a symlinked path was detected for a state directory.
	ErrSymlinkNotAllowed = errors.New("statedir: symlinks are not permitted for state directories")

	// ErrInsecurePermissions indicates a state directory has insecure or world-writable permissions.
	ErrInsecurePermissions = errors.New("statedir: insecure directory permissions")

	// ErrWSLHostPathResolution indicates failure to resolve a host-visible ProgramData path under WSL.
	ErrWSLHostPathResolution = errors.New("statedir: running under WSL but cannot safely resolve host-visible ProgramData state directory; specify --state-dir or AMC_STATE_DIR")
)

// StateDir manages the filesystem paths for local state storage.
type StateDir struct {
	root string
}

// Resolve returns a StateDir configured from an explicit flag, an environment variable, or a safe default.
func Resolve(explicitFlag string) (*StateDir, error) {
	var path string
	if explicitFlag != "" {
		path = strings.TrimSpace(explicitFlag)
	} else if envVal := os.Getenv(EnvStateDir); envVal != "" {
		path = strings.TrimSpace(envVal)
	} else {
		defaultPath, err := defaultStateDir()
		if err != nil {
			return nil, fmt.Errorf("failed to resolve default state directory: %w", err)
		}
		path = defaultPath
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path for state directory %q: %w", path, err)
	}

	sd := &StateDir{root: absPath}
	return sd, nil
}

// EnsureDirs creates the root state directory and all required subdirectories with restrictive 0700 permissions.
func (s *StateDir) EnsureDirs() error {
	subdirs := []string{
		s.root,
		s.LeasesDir(),
		s.ReceiptsDir(),
		s.AuditDir(),
		s.ApprovalsDir(),
		s.DaemonDir(),
		s.AuthDir(),
		s.OperationsDir(),
		s.SessionsDir(),
		s.KeysDir(),
		s.MachinesDir(),
	}

	for _, dir := range subdirs {
		if err := ensureSingleDir(dir); err != nil {
			return err
		}
	}
	return nil
}

func ensureSingleDir(dir string) error {
	if err := validateNoSymlinkComponents(dir); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	switch {
	case err == nil:
		return validateExistingDir(dir, fi)
	case os.IsNotExist(err):
		return createAndValidateDir(dir)
	default:
		return fmt.Errorf("failed to access state directory %q: %w", dir, err)
	}
}

func validateNoSymlinkComponents(path string) error {
	cleaned := filepath.Clean(path)
	vol := filepath.VolumeName(cleaned)
	rest := strings.TrimPrefix(cleaned, vol)
	parts := strings.Split(rest, string(filepath.Separator))

	current := vol
	if current == "" && filepath.IsAbs(cleaned) {
		current = string(filepath.Separator)
	}

	for _, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		fi, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				break
			}
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			canonical, allowed, aliasErr := allowedSystemPathAlias(current)
			if aliasErr != nil {
				return aliasErr
			}
			if allowed {
				current = canonical
				continue
			}
			return fmt.Errorf("%w: symlink component detected at %q", ErrSymlinkNotAllowed, current)
		}
	}
	return nil
}

func validateExistingDir(dir string, fi os.FileInfo) error {
	if err := validateNoSymlinkComponents(dir); err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("state path %q exists and is not a directory", dir)
	}
	if runtime.GOOS != "windows" {
		if fi.Mode().Perm() != DirPerm {
			if err := os.Chmod(dir, DirPerm); err != nil {
				return fmt.Errorf("%w: directory %q has mode %04o and chmod failed: %v", ErrInsecurePermissions, dir, fi.Mode().Perm(), err)
			}
		}
	}
	return ensurePlatformPrivateDirectory(dir)
}

func createAndValidateDir(dir string) error {
	if err := validateNoSymlinkComponents(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, DirPerm); err != nil {
		return fmt.Errorf("failed to create state directory %q: %w", dir, err)
	}
	if err := validateNoSymlinkComponents(dir); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dir, DirPerm); err != nil {
			return fmt.Errorf("%w: failed to enforce mode 0700 on %q: %v", ErrInsecurePermissions, dir, err)
		}
	}
	return ensurePlatformPrivateDirectory(dir)
}

// Root returns the root state directory path.
func (s *StateDir) Root() string {
	return s.root
}

// LeasesDir returns the path to the leases subdirectory.
func (s *StateDir) LeasesDir() string {
	return filepath.Join(s.root, SubdirLeases)
}

// ReceiptsDir returns the path to the receipts subdirectory.
func (s *StateDir) ReceiptsDir() string {
	return filepath.Join(s.root, SubdirReceipts)
}

// AuditDir returns the path to the audit subdirectory.
func (s *StateDir) AuditDir() string {
	return filepath.Join(s.root, SubdirAudit)
}

// ApprovalsDir returns the path to the approvals subdirectory.
func (s *StateDir) ApprovalsDir() string {
	return filepath.Join(s.root, SubdirApprovals)
}

// DaemonDir returns the path to the daemon subdirectory.
func (s *StateDir) DaemonDir() string {
	return filepath.Join(s.root, SubdirDaemon)
}

// AuthDir returns the path to the auth subdirectory.
func (s *StateDir) AuthDir() string {
	return filepath.Join(s.root, SubdirAuth)
}

// OperationsDir returns the path to the operations subdirectory.
func (s *StateDir) OperationsDir() string {
	return filepath.Join(s.root, SubdirOperations)
}

// SessionsDir returns the path to the sessions subdirectory.
func (s *StateDir) SessionsDir() string {
	return filepath.Join(s.root, SubdirSessions)
}

// KeysDir returns the path to the keys subdirectory.
func (s *StateDir) KeysDir() string {
	return filepath.Join(s.root, SubdirKeys)
}

// MachinesDir returns the path to the machines subdirectory.
func (s *StateDir) MachinesDir() string {
	return filepath.Join(s.root, SubdirMachines)
}

func defaultStateDir() (string, error) {
	if runtime.GOOS == "windows" || os.Getenv("AMC_TEST_WINDOWS") == "1" {
		return defaultWindowsStateDir(), nil
	}

	if runtime.GOOS == "linux" && isWSL() {
		return resolveWSLHostStateDir()
	}

	// Native POSIX default
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "amc"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "amc"), nil
}

func defaultWindowsStateDir() string {
	progData := os.Getenv("ProgramData")
	if progData == "" {
		progData = os.Getenv("ALLUSERSPROFILE")
	}
	if progData == "" {
		progData = `C:\ProgramData`
	}
	return filepath.Join(progData, "amc")
}

func isWSL() bool {
	if os.Getenv("AMC_TEST_NON_WSL") == "1" {
		return false
	}
	if os.Getenv("WSL_DISTRO_NAME") != "" || os.Getenv("WSL_INTEROP") != "" {
		return true
	}
	if data, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		s := strings.ToLower(string(data))
		if strings.Contains(s, "microsoft") || strings.Contains(s, "wsl") {
			return true
		}
	}
	if data, err := os.ReadFile("/proc/version"); err == nil {
		s := strings.ToLower(string(data))
		if strings.Contains(s, "microsoft") || strings.Contains(s, "wsl") {
			return true
		}
	}
	return false
}

func resolveWSLHostStateDir() (string, error) {
	if os.Getenv("AMC_TEST_WSL_FAIL") == "1" {
		return "", ErrWSLHostPathResolution
	}
	candidates := []string{
		"/mnt/c/ProgramData",
		"/c/ProgramData",
	}

	for _, cand := range candidates {
		fi, err := os.Stat(cand)
		if err == nil && fi.IsDir() {
			return filepath.Join(cand, "amc"), nil
		}
	}

	return "", ErrWSLHostPathResolution
}
