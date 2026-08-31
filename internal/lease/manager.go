package lease

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/Horcag/agent-machine-control/internal/statedir"
)

// GenerationRecord stores durable monotonic fencing generation metadata.
type GenerationRecord struct {
	SchemaVersion  string    `json:"schema_version"`
	MachineID      string    `json:"machine_id"`
	LastGeneration uint64    `json:"last_generation"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Manager manages host-visible file-backed leases for virtual machines.
type Manager struct {
	dir              string
	nowFn            func() time.Time
	livenessChecker  LivenessChecker
	identityProvider IdentityProvider
	ownerPrefix      string
	removeFn         func(string) error
}

// Option configures Manager behavior.
type Option func(*Manager)

// WithClock configures a custom clock.
func WithClock(fn func() time.Time) Option {
	return func(m *Manager) {
		m.nowFn = fn
	}
}

// WithLivenessChecker configures a custom liveness checker.
func WithLivenessChecker(checker LivenessChecker) Option {
	return func(m *Manager) {
		m.livenessChecker = checker
	}
}

// WithIdentityProvider configures a custom identity provider.
func WithIdentityProvider(provider IdentityProvider) Option {
	return func(m *Manager) {
		m.identityProvider = provider
	}
}

// WithOwnerPrefix sets a custom owner prefix (e.g. "direct" or "amcd").
func WithOwnerPrefix(prefix string) Option {
	return func(m *Manager) {
		m.ownerPrefix = prefix
	}
}

// WithRemoveFunc injects transition-lock cleanup removal for deterministic failure tests.
func WithRemoveFunc(fn func(string) error) Option {
	return func(m *Manager) { m.removeFn = fn }
}

// NewManager creates a new lease Manager for the given leases directory.
func NewManager(dir string, opts ...Option) *Manager {
	m := &Manager{
		dir:              dir,
		nowFn:            time.Now,
		livenessChecker:  &DefaultLivenessChecker{},
		identityProvider: &DefaultIdentityProvider{},
		ownerPrefix:      "direct",
		removeFn:         os.Remove,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

func (m *Manager) now() time.Time {
	if m.nowFn != nil {
		return m.nowFn().UTC()
	}
	return time.Now().UTC()
}

func (m *Manager) leasePath(machineID string) (string, error) {
	return m.stateFilePath(machineID, ".lease.json")
}

func (m *Manager) genPath(machineID string) (string, error) {
	return m.stateFilePath(machineID, ".gen.json")
}

func (m *Manager) stateFilePath(machineID, suffix string) (string, error) {
	filename := machineID + suffix
	if !filepath.IsLocal(filename) || filepath.Base(filename) != filename {
		return "", errors.New("lease: state file path escapes the leases directory")
	}
	return filepath.Join(m.dir, filename), nil
}

func (m *Manager) lockPath(machineID string) string {
	return filepath.Join(m.dir, fmt.Sprintf("%s.lock", machineID))
}

func validatedStateMachineID(machineID string) (string, error) {
	if machineID == "" {
		return "", errors.New("lease: machineID cannot be empty")
	}
	if machineID == "." || machineID == ".." || strings.ContainsAny(machineID, `/\\:`) || strings.IndexFunc(machineID, unicode.IsSpace) >= 0 || strings.IndexFunc(machineID, unicode.IsControl) >= 0 {
		return "", errors.New("lease: machineID must be a single local state path component")
	}
	if !filepath.IsLocal(machineID) || filepath.Base(machineID) != machineID {
		return "", errors.New("lease: machineID must be a single local state path component")
	}
	return machineID, nil
}

// Acquire attempts to acquire a host-visible lease on a machine.
func (m *Manager) Acquire(ctx context.Context, machineID string, opKind string, fingerprint string, ttl time.Duration) (*Lease, error) {
	validatedMachineID, err := validatedStateMachineID(machineID)
	if err != nil {
		return nil, err
	}
	machineID = validatedMachineID
	if ttl <= 0 {
		ttl = DefaultLeaseTTL
	}

	runtimeID, pid, startTime := m.identityProvider.CurrentIdentity()
	ownerToken, err := m.generateOwnerToken(pid)
	if err != nil {
		return nil, err
	}
	now := m.now()

	var acquiredLease *Lease
	err = m.withLock(ctx, machineID, func() error {
		path, pathErr := m.leasePath(machineID)
		if pathErr != nil {
			return pathErr
		}
		existing, err := m.readLeaseFile(path)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("%w: %v", ErrInvalidLeaseData, err)
		}

		lastGen, genErr := m.readGeneration(machineID)
		if genErr != nil {
			return fmt.Errorf("%w: %v", ErrInvalidLeaseData, genErr)
		}

		fencingGen, err := m.verifyCanAcquire(existing, lastGen, runtimeID, now)
		if err != nil {
			return err
		}

		newLease := &Lease{
			SchemaVersion:     SchemaVersion,
			MachineID:         machineID,
			OwnerID:           ownerToken,
			RuntimeID:         runtimeID,
			PID:               pid,
			ProcessStartTime:  startTime,
			OperationKind:     opKind,
			Fingerprint:       fingerprint,
			AcquiredAt:        now,
			ExpiresAt:         now.Add(ttl),
			FencingGeneration: fencingGen,
		}

		if err := m.writeLeaseFile(path, newLease); err != nil {
			return err
		}
		if err := m.writeGeneration(machineID, fencingGen, now); err != nil {
			return err
		}

		acquiredLease = newLease
		return nil
	})

	if err != nil {
		return nil, err
	}
	return acquiredLease, nil
}

// Release releases a held lease, verifying owner and fencing generation.
func (m *Manager) Release(ctx context.Context, l *Lease) error {
	if l == nil {
		return nil
	}
	validatedMachineID, err := validatedStateMachineID(l.MachineID)
	if err != nil {
		return err
	}

	return m.withLock(ctx, validatedMachineID, func() error {
		path, pathErr := m.leasePath(validatedMachineID)
		if pathErr != nil {
			return pathErr
		}
		existing, err := m.readLeaseFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}

		if existing.OwnerID != l.OwnerID || existing.FencingGeneration != l.FencingGeneration {
			return ErrLeaseFencingViolation
		}

		now := m.now()
		if err := m.writeGeneration(validatedMachineID, existing.FencingGeneration, now); err != nil {
			return fmt.Errorf("lease: failed to persist generation tombstone: %w", err)
		}

		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		if dirF, err := os.Open(m.dir); err == nil {
			_ = dirF.Sync()
			_ = dirF.Close()
		}
		return nil
	})
}

func (m *Manager) readGeneration(machineID string) (uint64, error) {
	path, err := m.genPath(machineID)
	if err != nil {
		return 0, err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if fi.Mode()&os.ModeSymlink != 0 || fi.Size() > 64*1024 {
		return 0, ErrInvalidLeaseData
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	var rec GenerationRecord
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil {
		return 0, ErrInvalidLeaseData
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return 0, ErrInvalidLeaseData
	}
	if rec.SchemaVersion != SchemaVersion || rec.MachineID != machineID {
		return 0, ErrInvalidLeaseData
	}
	return rec.LastGeneration, nil
}

func (m *Manager) writeGeneration(machineID string, gen uint64, now time.Time) error {
	path, err := m.genPath(machineID)
	if err != nil {
		return err
	}
	rec := GenerationRecord{
		SchemaVersion:  SchemaVersion,
		MachineID:      machineID,
		LastGeneration: gen,
		UpdatedAt:      now,
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return m.writeAtomicFile(path, data)
}

func (m *Manager) readLeaseFile(path string) (*Lease, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 || fi.Size() > 64*1024 {
		return nil, ErrInvalidLeaseData
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var l Lease
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return nil, ErrInvalidLeaseData
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, ErrInvalidLeaseData
	}
	if l.SchemaVersion != SchemaVersion || l.MachineID == "" || l.OwnerID == "" {
		return nil, ErrInvalidLeaseData
	}
	return &l, nil
}

func (m *Manager) writeLeaseFile(path string, l *Lease) error {
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return m.writeAtomicFile(path, data)
}

func (m *Manager) writeAtomicFile(path string, data []byte) error {
	tmpPath := fmt.Sprintf("%s.tmp.%d", path, time.Now().UnixNano())
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	_ = f.Close()
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := statedir.SyncDir(m.dir); err != nil {
		return fmt.Errorf("lease: failed to sync leases directory: %w", err)
	}
	return nil
}

func (m *Manager) generateOwnerToken(pid int) (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("lease: failed to generate random owner token: %w", err)
	}
	return fmt.Sprintf("%s:%d:%s", m.ownerPrefix, pid, hex.EncodeToString(b)), nil
}

func (m *Manager) verifyCanAcquire(existing *Lease, lastGen uint64, runtimeID string, now time.Time) (uint64, error) {
	baseGen := lastGen
	if existing != nil {
		if existing.FencingGeneration > baseGen {
			baseGen = existing.FencingGeneration
		}
		if now.Before(existing.ExpiresAt) {
			return 0, ErrLeaseConflict
		}
		if existing.RuntimeID == "" || existing.RuntimeID != runtimeID {
			return 0, ErrLeaseUnverifiableOwner
		}
		alive, checkErr := m.livenessChecker.IsAlive(existing.PID, existing.ProcessStartTime)
		if checkErr != nil || alive {
			return 0, ErrLeaseConflict
		}
	}
	if baseGen == ^uint64(0) {
		return 0, errors.New("lease: fencing generation overflow")
	}
	return baseGen + 1, nil
}
