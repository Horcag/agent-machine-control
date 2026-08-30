package ssh

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	gossh "golang.org/x/crypto/ssh"
)

// KeyProvider resolves SSH credentials and host-key pinning policies.
type KeyProvider interface {
	GetClientSigner(ctx context.Context, target domain.MachineRef) (gossh.Signer, error)
	GetHostKeyCallback(ctx context.Context, target domain.MachineRef) (gossh.HostKeyCallback, error)
	GetGuestUser(ctx context.Context, target domain.MachineRef) (string, error)
	GetGuestEndpoint(ctx context.Context, target domain.MachineRef) (string, error)
	GetMachineConfig(target domain.MachineRef) (*MachineSSHConfig, error)
}

// MachineSSHConfig represents persisted connection metadata for a target VM.
type MachineSSHConfig struct {
	Endpoint                    string `json:"endpoint"`
	User                        string `json:"user"`
	DefaultKeyAlias             string `json:"default_key_alias"`
	PinnedHostKeySHA256         string `json:"pinned_host_key_sha256"`
	ExternalEffectsContained    bool   `json:"external_effects_contained,omitempty"`
	RollbackCheckpointID        string `json:"rollback_checkpoint_id,omitempty"`
	RequireProductionCheckpoint bool   `json:"require_production_checkpoint,omitempty"`
}

// LocalKeyProvider resolves keys and configurations stored in the state directory.
type LocalKeyProvider struct {
	stateDir *statedir.StateDir
}

// NewLocalKeyProvider creates a LocalKeyProvider from a StateDir.
func NewLocalKeyProvider(sd *statedir.StateDir) *LocalKeyProvider {
	return &LocalKeyProvider{stateDir: sd}
}

func validateStrictFile(path string) ([]byte, error) {
	cleaned := filepath.Clean(path)
	parts := strings.Split(cleaned, string(filepath.Separator))
	current := ""
	if filepath.IsAbs(cleaned) {
		current = string(filepath.Separator)
	}

	for _, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		fi, err := os.Lstat(current)
		if err != nil {
			return nil, err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("security: symlink component detected at %q", current)
		}
	}

	fi, err := os.Stat(cleaned)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("security: %q is not a regular file", cleaned)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("security: file %q has insecure permissions %04o; must be 0600", cleaned, fi.Mode().Perm())
	}

	return os.ReadFile(cleaned)
}

// GetMachineConfig loads the server-owned machine configuration.
func (p *LocalKeyProvider) GetMachineConfig(target domain.MachineRef) (*MachineSSHConfig, error) {
	if p.stateDir == nil {
		return nil, errors.New("ssh: state directory is unconfigured")
	}
	targetID := string(target)
	if err := domain.ValidateMachineGUID(targetID); err != nil {
		return nil, fmt.Errorf("ssh: invalid target machine GUID: %w", err)
	}

	cfgPath := filepath.Join(p.stateDir.MachinesDir(), targetID, "config.json")
	data, err := validateStrictFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: machine config not found for %s", domain.ErrSessionNotFound, targetID)
		}
		return nil, fmt.Errorf("ssh: failed to read machine config: %w", err)
	}

	var cfg MachineSSHConfig
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("ssh: malformed machine config: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, errors.New("ssh: machine config contains trailing or malformed data")
	}
	return &cfg, nil
}

// GetClientSigner loads and parses a private key file for the target.
func (p *LocalKeyProvider) GetClientSigner(_ context.Context, target domain.MachineRef) (gossh.Signer, error) {
	if p.stateDir == nil {
		return nil, errors.New("ssh: state directory is unconfigured")
	}
	alias := "default"
	cfg, err := p.GetMachineConfig(target)
	if err == nil && cfg.DefaultKeyAlias != "" {
		alias = cfg.DefaultKeyAlias
	}

	if err := validateKeyAlias(alias); err != nil {
		return nil, fmt.Errorf("ssh: invalid key alias: %w", err)
	}

	data, err := loadPrivateKeyMaterial(p.stateDir.KeysDir(), alias)
	if err != nil {
		return nil, fmt.Errorf("ssh: failed to load protected private key for configured alias: %w", err)
	}
	defer zeroBytes(data)

	signer, err := gossh.ParsePrivateKey(data)
	if err != nil {
		return nil, errors.New("ssh: failed to parse protected private key for configured alias")
	}

	return signer, nil
}

func zeroBytes(data []byte) {
	for i := range data {
		data[i] = 0
	}
}

// GetHostKeyCallback constructs a strict host key pinning validator.
func (p *LocalKeyProvider) GetHostKeyCallback(_ context.Context, target domain.MachineRef) (gossh.HostKeyCallback, error) {
	cfg, err := p.GetMachineConfig(target)
	if err != nil {
		return nil, err
	}

	pinnedPin := strings.TrimSpace(cfg.PinnedHostKeySHA256)
	if pinnedPin == "" {
		return nil, fmt.Errorf("%w: target machine %s has no pinned host key", domain.ErrMissingHostKeyPin, target)
	}

	return func(_ string, _ net.Addr, key gossh.PublicKey) error {
		sum := sha256.Sum256(key.Marshal())
		actualPin := base64.StdEncoding.EncodeToString(sum[:])

		if subtle.ConstantTimeCompare([]byte(actualPin), []byte(pinnedPin)) != 1 {
			return fmt.Errorf("%w: host key mismatch for target machine %s (expected pin match)", domain.ErrHostKeyMismatch, target)
		}
		return nil
	}, nil
}

// GetGuestUser returns the effective SSH username for the target VM.
func (p *LocalKeyProvider) GetGuestUser(_ context.Context, target domain.MachineRef) (string, error) {
	cfg, err := p.GetMachineConfig(target)
	if err == nil && cfg.User != "" {
		return cfg.User, nil
	}
	return "Administrator", nil
}

// GetGuestEndpoint returns the connection address (e.g. 192.168.100.2:22 or 127.0.0.1:2222) for the target VM.
func (p *LocalKeyProvider) GetGuestEndpoint(_ context.Context, target domain.MachineRef) (string, error) {
	cfg, err := p.GetMachineConfig(target)
	if err != nil {
		return "", err
	}
	if cfg.Endpoint == "" {
		return "", fmt.Errorf("%w: machine config missing endpoint", domain.ErrSessionNotFound)
	}
	return cfg.Endpoint, nil
}

// MockKeyProvider implements KeyProvider for hermetic tests.
type MockKeyProvider struct {
	Signer          gossh.Signer
	PinnedKeySHA256 string
	User            string
	Endpoint        string
	FailPinMissing  bool
	FailPinMismatch bool
	MachineConfig   *MachineSSHConfig
}

func (m *MockKeyProvider) GetMachineConfig(_ domain.MachineRef) (*MachineSSHConfig, error) {
	if m.MachineConfig != nil {
		return m.MachineConfig, nil
	}
	return &MachineSSHConfig{
		Endpoint:            m.Endpoint,
		User:                m.User,
		DefaultKeyAlias:     "default",
		PinnedHostKeySHA256: m.PinnedKeySHA256,
	}, nil
}

func (m *MockKeyProvider) GetClientSigner(_ context.Context, _ domain.MachineRef) (gossh.Signer, error) {
	if m.Signer == nil {
		return nil, errors.New("mock: no signer configured")
	}
	return m.Signer, nil
}

func (m *MockKeyProvider) GetHostKeyCallback(_ context.Context, _ domain.MachineRef) (gossh.HostKeyCallback, error) {
	if m.FailPinMissing || m.PinnedKeySHA256 == "" {
		return nil, domain.ErrMissingHostKeyPin
	}
	return func(_ string, _ net.Addr, key gossh.PublicKey) error {
		if m.FailPinMismatch {
			return domain.ErrHostKeyMismatch
		}
		sum := sha256.Sum256(key.Marshal())
		actualPin := base64.StdEncoding.EncodeToString(sum[:])
		if subtle.ConstantTimeCompare([]byte(actualPin), []byte(m.PinnedKeySHA256)) != 1 {
			return domain.ErrHostKeyMismatch
		}
		return nil
	}, nil
}

func (m *MockKeyProvider) GetGuestUser(_ context.Context, _ domain.MachineRef) (string, error) {
	if m.User != "" {
		return m.User, nil
	}
	return "testuser", nil
}

func (m *MockKeyProvider) GetGuestEndpoint(_ context.Context, _ domain.MachineRef) (string, error) {
	if m.Endpoint == "" {
		return "127.0.0.1:22", nil
	}
	return m.Endpoint, nil
}
