package receipt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

var (
	// ErrIdempotencyCollision indicates an idempotency key was reused with different actor, target, or parameters.
	ErrIdempotencyCollision = errors.New("receipt: idempotency key collision with existing operation")

	// ErrReceiptNotFound indicates no receipt matches the requested ID.
	ErrReceiptNotFound = errors.New("receipt: receipt not found")

	// ErrReceiptCollision indicates an existing receipt ID contains different immutable data.
	ErrReceiptCollision = errors.New("receipt: receipt ID collision with different terminal record")
)

// DTO is the on-disk and machine-readable JSON representation of a domain.Receipt.
type DTO struct {
	ReceiptID              string                 `json:"receipt_id"`
	OperationKind          string                 `json:"operation_kind"`
	Fingerprint            string                 `json:"fingerprint"`
	IdempotencyFingerprint string                 `json:"idempotency_fingerprint,omitempty"`
	IdempotencyKey         string                 `json:"idempotency_key,omitempty"`
	Actor                  string                 `json:"actor"`
	Target                 string                 `json:"target"`
	Class                  domain.OperationClass  `json:"class"`
	EffectiveBackend       string                 `json:"effective_backend"`
	StartedAt              string                 `json:"started_at"`
	CompletedAt            string                 `json:"completed_at"`
	Outcome                OutcomeDTO             `json:"outcome"`
	ObservationType        domain.ObservationType `json:"observation_type"`
	EvidenceRefs           []string               `json:"evidence_refs,omitempty"`
	RollbackRef            string                 `json:"rollback_ref,omitempty"`
	RedactionStatus        domain.RedactionStatus `json:"redaction_status"`
}

// OutcomeDTO is the outcome structure inside DTO.
type OutcomeDTO struct {
	Status        domain.OutcomeStatus `json:"status"`
	ExitCode      int                  `json:"exit_code"`
	ErrorCategory string               `json:"error_category,omitempty"`
	ErrorMessage  string               `json:"error_message,omitempty"`
}

// ConvertToDTO converts a domain.Receipt into a DTO.
func ConvertToDTO(r domain.Receipt) DTO {
	return DTO{
		ReceiptID:              string(r.ReceiptID),
		OperationKind:          string(r.OperationKind),
		Fingerprint:            string(r.Fingerprint),
		IdempotencyFingerprint: string(r.IdempotencyFingerprint),
		IdempotencyKey:         r.IdempotencyKey,
		Actor:                  string(r.Actor),
		Target:                 string(r.Target),
		Class:                  r.Class,
		EffectiveBackend:       r.EffectiveBackend,
		StartedAt:              r.StartedAt.UTC().Format(time.RFC3339Nano),
		CompletedAt:            r.CompletedAt.UTC().Format(time.RFC3339Nano),
		Outcome: OutcomeDTO{
			Status:        r.Outcome.Status,
			ExitCode:      r.Outcome.ExitCode,
			ErrorCategory: r.Outcome.ErrorCategory,
			ErrorMessage:  r.Outcome.ErrorMessage,
		},
		ObservationType: r.ObservationType,
		EvidenceRefs:    r.EvidenceRefs,
		RollbackRef:     r.RollbackRef,
		RedactionStatus: r.RedactionStatus,
	}
}

// ConvertFromDTO converts a DTO back to a domain.Receipt.
func ConvertFromDTO(dto DTO) (domain.Receipt, error) {
	startedAt, err := time.Parse(time.RFC3339Nano, dto.StartedAt)
	if err != nil {
		startedAt, err = time.Parse(time.RFC3339, dto.StartedAt)
		if err != nil {
			return domain.Receipt{}, fmt.Errorf("receipt: invalid started_at timestamp: %w", err)
		}
	}

	completedAt, err := time.Parse(time.RFC3339Nano, dto.CompletedAt)
	if err != nil {
		completedAt, err = time.Parse(time.RFC3339, dto.CompletedAt)
		if err != nil {
			return domain.Receipt{}, fmt.Errorf("receipt: invalid completed_at timestamp: %w", err)
		}
	}

	return domain.Receipt{
		ReceiptID:              domain.ReceiptID(dto.ReceiptID),
		OperationKind:          domain.OperationKind(dto.OperationKind),
		Fingerprint:            domain.Fingerprint(dto.Fingerprint),
		IdempotencyFingerprint: domain.Fingerprint(dto.IdempotencyFingerprint),
		IdempotencyKey:         dto.IdempotencyKey,
		Actor:                  domain.ActorID(dto.Actor),
		Target:                 domain.MachineRef(dto.Target),
		Class:                  dto.Class,
		EffectiveBackend:       dto.EffectiveBackend,
		StartedAt:              startedAt,
		CompletedAt:            completedAt,
		Outcome: domain.ExecutionOutcome{
			Status:        dto.Outcome.Status,
			ExitCode:      dto.Outcome.ExitCode,
			ErrorCategory: dto.Outcome.ErrorCategory,
			ErrorMessage:  dto.Outcome.ErrorMessage,
		},
		ObservationType: dto.ObservationType,
		EvidenceRefs:    dto.EvidenceRefs,
		RollbackRef:     dto.RollbackRef,
		RedactionStatus: dto.RedactionStatus,
	}, nil
}

// Option configures Store dependencies.
type Option func(*Store)

// WithSaveHook injects a pre-save failure hook for deterministic durability tests.
func WithSaveHook(fn func(domain.Receipt) error) Option {
	return func(s *Store) { s.saveHook = fn }
}

// WithSyncDir configures a custom directory sync function on Store.
func WithSyncDir(fn func(dir string) error) Option {
	return func(s *Store) {
		s.syncDirFn = fn
	}
}

// Store manages persistence and idempotency retrieval of domain.Receipt records.
type Store struct {
	dir        string
	syncDirFn  func(dir string) error
	saveHook   func(domain.Receipt) error
	lookupHook func(context.Context) error
	mu         sync.RWMutex
}

// WithLookupHook injects a context-aware lookup boundary for deterministic deadline tests.
func WithLookupHook(fn func(context.Context) error) Option {
	return func(s *Store) { s.lookupHook = fn }
}

// NewStore creates a new Receipt Store for the given receipts directory.
func NewStore(dir string, opts ...Option) *Store {
	s := &Store{
		dir:       dir,
		syncDirFn: statedir.SyncDir,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// CheckWritable verifies that a new durable receipt can be created in the store.
func (s *Store) CheckWritable() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	probe := filepath.Join(s.dir, fmt.Sprintf(".write-test-%d", time.Now().UnixNano()))
	f, err := os.OpenFile(probe, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(probe)
		return err
	}
	if err := os.Remove(probe); err != nil {
		return err
	}
	return statedir.SyncDir(s.dir)
}

// MaxReceiptFileSize is the maximum allowed receipt file size (64 KB).
const MaxReceiptFileSize = 64 * 1024

// Save persists a domain.Receipt atomically to disk with 0600 permissions.
func (s *Store) Save(r domain.Receipt) error {
	if err := r.Validate(); err != nil {
		return fmt.Errorf("receipt: cannot save invalid receipt: %w", err)
	}
	if s.saveHook != nil {
		if err := s.saveHook(r); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	dto := ConvertToDTO(r)
	data, err := json.MarshalIndent(dto, "", "  ")
	if err != nil {
		return fmt.Errorf("receipt: failed to marshal receipt: %w", err)
	}

	finalPath := filepath.Join(s.dir, fmt.Sprintf("%s.json", r.ReceiptID))
	if _, err := os.Lstat(finalPath); err == nil {
		return fmt.Errorf("receipt: receipt file %q already exists; cannot overwrite existing receipt", finalPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("receipt: failed to check receipt file %q: %w", finalPath, err)
	}

	tmpPath := filepath.Join(s.dir, fmt.Sprintf("%s.tmp.%d", r.ReceiptID, time.Now().UnixNano()))

	if err := s.writeReceiptTemp(tmpPath, data); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("receipt: failed to write receipt temp file: %w", err)
	}

	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("receipt: failed to commit receipt file: %w", err)
	}

	syncFn := s.syncDirFn
	if syncFn == nil {
		syncFn = statedir.SyncDir
	}
	if err := syncFn(s.dir); err != nil {
		return fmt.Errorf("receipt: failed to sync directory %q: %w", s.dir, err)
	}

	return nil
}

// Ensure idempotently persists the exact receipt and rejects conflicting reuse of its ID.
func (s *Store) Ensure(r domain.Receipt) error {
	if err := r.Validate(); err != nil {
		return fmt.Errorf("receipt: cannot ensure invalid receipt: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	finalPath := filepath.Join(s.dir, fmt.Sprintf("%s.json", r.ReceiptID))
	existing, err := s.readReceiptFile(finalPath)
	if err == nil {
		if reflect.DeepEqual(ConvertToDTO(*existing), ConvertToDTO(r)) {
			return nil
		}
		return ErrReceiptCollision
	}
	if !os.IsNotExist(err) {
		return err
	}
	if s.saveHook != nil {
		if err := s.saveHook(r); err != nil {
			return err
		}
	}

	data, err := json.MarshalIndent(ConvertToDTO(r), "", "  ")
	if err != nil {
		return fmt.Errorf("receipt: failed to marshal receipt: %w", err)
	}
	tmpPath := filepath.Join(s.dir, fmt.Sprintf("%s.tmp.%d", r.ReceiptID, time.Now().UnixNano()))
	if err := s.writeReceiptTemp(tmpPath, data); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("receipt: failed to write receipt temp file: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("receipt: failed to commit receipt file: %w", err)
	}
	syncFn := s.syncDirFn
	if syncFn == nil {
		syncFn = statedir.SyncDir
	}
	if err := syncFn(s.dir); err != nil {
		return fmt.Errorf("receipt: failed to sync directory %q: %w", s.dir, err)
	}
	return nil
}

func (s *Store) writeReceiptTemp(tmpPath string, data []byte) error {
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return f.Close()
}

// LookupIdempotency checks for prior executions of an operation by idempotency key.
// Returns:
// - (*domain.Receipt, nil) if an exact retry match is found.
// - (nil, nil) if no prior record with this key exists.
// - (nil, ErrIdempotencyCollision) if the key exists but actor, target, or parameters differ.
func (s *Store) LookupIdempotency(op domain.Operation) (*domain.Receipt, error) {
	return s.LookupIdempotencyContext(context.Background(), op)
}

// LookupIdempotencyContext checks prior executions within the caller's deadline.
func (s *Store) LookupIdempotencyContext(ctx context.Context, op domain.Operation) (*domain.Receipt, error) {
	if op.IdempotencyKey == "" {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.lookupHook != nil {
		if err := s.lookupHook(ctx); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	fp, err := op.Fingerprint()
	if err != nil {
		return nil, fmt.Errorf("receipt: failed to compute fingerprint: %w", err)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("receipt: failed to read receipts directory: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return s.findIdempotentReceipt(ctx, entries, op, fp)
}

func (s *Store) findIdempotentReceipt(ctx context.Context, entries []os.DirEntry, op domain.Operation, fp domain.Fingerprint) (*domain.Receipt, error) {
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.Contains(entry.Name(), ".tmp.") {
			continue
		}

		filePath := filepath.Join(s.dir, entry.Name())
		matched, rcpt, err := s.inspectReceiptFileContext(ctx, filePath, op, fp)
		if err != nil {
			return nil, err
		}
		if matched {
			return rcpt, nil
		}
	}
	return nil, nil
}

func matchReceipt(rcpt domain.Receipt, op domain.Operation, fp domain.Fingerprint) (bool, error) {
	if rcpt.IdempotencyKey != op.IdempotencyKey {
		return false, nil
	}
	var match bool
	if rcpt.IdempotencyFingerprint == "" {
		match = (rcpt.Fingerprint == fp)
	} else {
		idFp, err := domain.ComputeIdempotencyFingerprint(op)
		if err != nil {
			return false, fmt.Errorf("receipt: failed to compute idempotency fingerprint: %w", err)
		}
		match = (rcpt.IdempotencyFingerprint == idFp)
	}
	if rcpt.Actor != op.Actor.EffectiveActor || rcpt.Target != op.Target || !match {
		return false, ErrIdempotencyCollision
	}
	return true, nil
}

func (s *Store) inspectReceiptFileContext(ctx context.Context, filePath string, op domain.Operation, fp domain.Fingerprint) (bool, *domain.Receipt, error) {
	receipt, err := s.readReceiptFileContext(ctx, filePath)
	if os.IsNotExist(err) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	matched, err := matchReceipt(*receipt, op, fp)
	if err != nil {
		return false, nil, err
	}
	if !matched {
		return false, nil, nil
	}
	if err := ctx.Err(); err != nil {
		return false, nil, err
	}

	return true, receipt, nil
}
