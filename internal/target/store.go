package target

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/Horcag/agent-machine-control/internal/statedir"
)

// Publication reports the effect and durability of the requested authority.
type Publication struct {
	Committed bool
	Durable   bool
}

// CommitResult is the effect truth returned by an injected namespace mutation.
type CommitResult struct {
	Committed bool
	Err       error
}

// Operations provides bounded fault-injection seams for atomic publication tests.
type Operations struct {
	Replace  func(context.Context, string, string) CommitResult
	Remove   func(string) CommitResult
	SyncDir  func(string) error
	ReadFile func(context.Context, string) ([]byte, error)
}

// Option configures a Store.
type Option func(*Store)

// WithOperations replaces selected filesystem operations. Nil fields retain secure defaults.
func WithOperations(operations Operations) Option {
	return func(store *Store) {
		if operations.Replace != nil {
			store.operations.Replace = operations.Replace
		}
		if operations.Remove != nil {
			store.operations.Remove = operations.Remove
		}
		if operations.SyncDir != nil {
			store.operations.SyncDir = operations.SyncDir
		}
		if operations.ReadFile != nil {
			store.operations.ReadFile = operations.ReadFile
		}
	}
}

// WithSecurity replaces platform security checks for focused fault-injection tests.
func WithSecurity(security Security) Option {
	return func(store *Store) {
		if security != nil {
			store.security = security
		}
	}
}

type pendingKind uint8

const (
	pendingSave pendingKind = iota + 1
	pendingClear
)

type pendingEffect struct {
	kind    pendingKind
	value   Default
	payload []byte
}

// Store owns one protected canonical target document.
type Store struct {
	mu         sync.Mutex
	dir        string
	path       string
	security   Security
	operations Operations
	pending    *pendingEffect
}

// NewStore constructs a store rooted in the dedicated statedir target directory.
func NewStore(dir string, options ...Option) (*Store, error) {
	if dir == "" || !filepath.IsAbs(dir) {
		return nil, errors.New("target: store directory must be an absolute path")
	}
	store := &Store{dir: filepath.Clean(dir), security: newPlatformSecurity()}
	store.path = filepath.Join(store.dir, StateFileName)
	store.operations = Operations{
		Replace: atomicReplace,
		Remove: func(path string) CommitResult {
			if err := os.Remove(path); err != nil {
				return CommitResult{Err: err}
			}
			return CommitResult{Committed: true}
		},
		SyncDir: statedir.SyncDir,
	}
	for _, option := range options {
		option(store)
	}
	if store.operations.ReadFile == nil {
		store.operations.ReadFile = store.readProtectedFile
	}
	return store, nil
}

// Load reads and strictly validates the currently committed default.
func (s *Store) Load(ctx context.Context) (Default, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.repairPending(ctx); err != nil {
		return Default{}, err
	}
	return s.loadDurably(ctx)
}

// Save atomically publishes one canonical target authority.
func (s *Store) Save(ctx context.Context, value Default) (Publication, error) {
	canonical, err := NewDefault(value.Locator, value.Aliases)
	if err != nil {
		return Publication{}, err
	}
	payload, err := encode(canonical)
	if err != nil {
		return Publication{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending != nil {
		if s.pending.kind != pendingSave || !s.pending.value.equal(canonical) {
			return Publication{}, ErrDurabilityPending
		}
		if err := s.prepareDirectory(ctx); err != nil {
			return Publication{Committed: true}, errors.Join(ErrCommittedNotDurable, err)
		}
		return s.repairSaved(ctx, canonical, payload)
	}
	if err := s.prepareDirectory(ctx); err != nil {
		return Publication{}, err
	}
	existing, err := s.loadDurably(ctx)
	switch {
	case err == nil && existing.equal(canonical):
		return Publication{Committed: true, Durable: true}, nil
	case err == nil:
	case errors.Is(err, ErrNoDefault):
	default:
		return Publication{}, err
	}
	return s.publish(ctx, canonical, payload)
}

// Clear removes the enrolled authority with explicit commit and durability truth.
func (s *Store) Clear(ctx context.Context) (Publication, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending != nil {
		if s.pending.kind != pendingClear {
			return Publication{}, ErrDurabilityPending
		}
		if err := s.validateDirectory(ctx); err != nil {
			return Publication{Committed: true}, errors.Join(ErrCommittedNotDurable, err)
		}
		return s.repairCleared(ctx)
	}
	if _, err := s.loadDurably(ctx); errors.Is(err, ErrNoDefault) {
		return Publication{Committed: true, Durable: true}, nil
	} else if err != nil {
		return Publication{}, err
	}
	if err := ctx.Err(); err != nil {
		return Publication{}, err
	}
	result := s.operations.Remove(s.path)
	if result.Err != nil {
		if result.Committed {
			s.pending = &pendingEffect{kind: pendingClear}
			return Publication{Committed: true}, errors.Join(ErrCommittedNotDurable, result.Err)
		}
		return Publication{}, result.Err
	}
	if !result.Committed {
		return Publication{}, ErrAtomicCommitUncertain
	}
	s.pending = &pendingEffect{kind: pendingClear}
	return s.repairCleared(ctx)
}

func (s *Store) publish(ctx context.Context, value Default, payload []byte) (Publication, error) {
	if err := ctx.Err(); err != nil {
		return Publication{}, err
	}
	temporary, err := os.CreateTemp(s.dir, ".target-*.tmp")
	if err != nil {
		return Publication{}, fmt.Errorf("target: create temporary state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := s.prepareTemporary(ctx, temporary, temporaryPath, payload); err != nil {
		return Publication{}, err
	}
	if err := ctx.Err(); err != nil {
		return Publication{}, err
	}
	result := s.operations.Replace(ctx, temporaryPath, s.path)
	if result.Err != nil {
		if result.Committed {
			s.rememberSave(value, payload)
			return Publication{Committed: true}, errors.Join(ErrCommittedNotDurable, result.Err)
		}
		return Publication{}, result.Err
	}
	if !result.Committed {
		return Publication{}, ErrAtomicCommitUncertain
	}
	s.pending = &pendingEffect{kind: pendingSave, value: value.Clone(), payload: bytes.Clone(payload)}
	return s.repairSaved(ctx, value, payload)
}

func (s *Store) prepareTemporary(ctx context.Context, file *os.File, path string, payload []byte) error {
	if err := s.security.ValidateInheritedFile(ctx, path); err != nil {
		return fmt.Errorf("%w: inherited temporary file: %w", ErrInsecureState, err)
	}
	if err := file.Chmod(0600); err != nil {
		return fmt.Errorf("target: protect temporary state mode: %w", err)
	}
	if err := s.security.ProtectNewFile(ctx, path); err != nil {
		return fmt.Errorf("%w: temporary file: %w", ErrInsecureState, err)
	}
	if _, err := file.Write(payload); err != nil {
		return fmt.Errorf("target: write temporary state: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("target: sync temporary state: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("target: close temporary state: %w", err)
	}
	if err := s.security.ValidateFile(ctx, path); err != nil {
		return fmt.Errorf("%w: temporary file: %w", ErrInsecureState, err)
	}
	return nil
}

func (s *Store) loadDurably(ctx context.Context) (Default, error) {
	if err := s.validateDirectory(ctx); err != nil {
		return Default{}, err
	}
	if err := s.operations.SyncDir(s.dir); err != nil {
		return Default{}, fmt.Errorf("target: synchronize directory before read: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Default{}, err
	}
	payload, err := s.operations.ReadFile(ctx, s.path)
	if errors.Is(err, os.ErrNotExist) {
		return Default{}, ErrNoDefault
	}
	if err != nil {
		return Default{}, fmt.Errorf("target: read canonical state: %w", err)
	}
	value, err := decode(payload)
	if err != nil {
		return Default{}, err
	}
	return value, nil
}

func (s *Store) prepareDirectory(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.security.ProtectDir(ctx, s.dir); err != nil {
		return fmt.Errorf("%w: protect directory: %w", ErrInsecureState, err)
	}
	return s.validateDirectory(ctx)
}

func (s *Store) validateDirectory(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.security.ValidateDir(ctx, s.dir); err != nil {
		return fmt.Errorf("%w: directory: %w", ErrInsecureState, err)
	}
	return nil
}

func (s *Store) readProtectedFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.security.ValidateFile(ctx, path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: canonical file: %w", ErrInsecureState, err)
	}
	file, err := openNoFollow(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, MaxDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > MaxDocumentBytes {
		return nil, fmt.Errorf("%w: document exceeds %d bytes", ErrInvalidDocument, MaxDocumentBytes)
	}
	return payload, nil
}
