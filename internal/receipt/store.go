package receipt

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
)

// DTO is the on-disk and machine-readable JSON representation of a domain.Receipt.
type DTO struct {
	ReceiptID        string                 `json:"receipt_id"`
	OperationKind    string                 `json:"operation_kind"`
	Fingerprint      string                 `json:"fingerprint"`
	IdempotencyKey   string                 `json:"idempotency_key,omitempty"`
	Actor            string                 `json:"actor"`
	Target           string                 `json:"target"`
	Class            domain.OperationClass  `json:"class"`
	EffectiveBackend string                 `json:"effective_backend"`
	StartedAt        string                 `json:"started_at"`
	CompletedAt      string                 `json:"completed_at"`
	Outcome          OutcomeDTO             `json:"outcome"`
	ObservationType  domain.ObservationType `json:"observation_type"`
	EvidenceRefs     []string               `json:"evidence_refs,omitempty"`
	RollbackRef      string                 `json:"rollback_ref,omitempty"`
	RedactionStatus  domain.RedactionStatus `json:"redaction_status"`
}

// OutcomeDTO is the outcome structure inside DTO.
type OutcomeDTO struct {
	Status   domain.OutcomeStatus `json:"status"`
	ExitCode int                  `json:"exit_code"`
}

// ConvertToDTO converts a domain.Receipt into a DTO.
func ConvertToDTO(r domain.Receipt) DTO {
	return DTO{
		ReceiptID:        string(r.ReceiptID),
		OperationKind:    string(r.OperationKind),
		Fingerprint:      string(r.Fingerprint),
		IdempotencyKey:   r.IdempotencyKey,
		Actor:            string(r.Actor),
		Target:           string(r.Target),
		Class:            r.Class,
		EffectiveBackend: r.EffectiveBackend,
		StartedAt:        r.StartedAt.UTC().Format(time.RFC3339Nano),
		CompletedAt:      r.CompletedAt.UTC().Format(time.RFC3339Nano),
		Outcome: OutcomeDTO{
			Status:   r.Outcome.Status,
			ExitCode: r.Outcome.ExitCode,
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
		ReceiptID:        domain.ReceiptID(dto.ReceiptID),
		OperationKind:    domain.OperationKind(dto.OperationKind),
		Fingerprint:      domain.Fingerprint(dto.Fingerprint),
		IdempotencyKey:   dto.IdempotencyKey,
		Actor:            domain.ActorID(dto.Actor),
		Target:           domain.MachineRef(dto.Target),
		Class:            dto.Class,
		EffectiveBackend: dto.EffectiveBackend,
		StartedAt:        startedAt,
		CompletedAt:      completedAt,
		Outcome: domain.ExecutionOutcome{
			Status:   dto.Outcome.Status,
			ExitCode: dto.Outcome.ExitCode,
		},
		ObservationType: dto.ObservationType,
		EvidenceRefs:    dto.EvidenceRefs,
		RollbackRef:     dto.RollbackRef,
		RedactionStatus: dto.RedactionStatus,
	}, nil
}

// Option configures Store dependencies.
type Option func(*Store)

// WithSyncDir configures a custom directory sync function on Store.
func WithSyncDir(fn func(dir string) error) Option {
	return func(s *Store) {
		s.syncDirFn = fn
	}
}

// Store manages persistence and idempotency retrieval of domain.Receipt records.
type Store struct {
	dir       string
	syncDirFn func(dir string) error
	mu        sync.RWMutex
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

// MaxReceiptFileSize is the maximum allowed receipt file size (64 KB).
const MaxReceiptFileSize = 64 * 1024

// Save persists a domain.Receipt atomically to disk with 0600 permissions.
func (s *Store) Save(r domain.Receipt) error {
	if err := r.Validate(); err != nil {
		return fmt.Errorf("receipt: cannot save invalid receipt: %w", err)
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
	if op.IdempotencyKey == "" {
		return nil, nil
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

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.Contains(entry.Name(), ".tmp.") {
			continue
		}

		filePath := filepath.Join(s.dir, entry.Name())
		matched, rcpt, err := s.inspectReceiptFile(filePath, op, fp)
		if err != nil {
			return nil, err
		}
		if matched {
			return rcpt, nil
		}
	}

	return nil, nil
}

func (s *Store) inspectReceiptFile(filePath string, op domain.Operation, fp domain.Fingerprint) (bool, *domain.Receipt, error) {
	fi, err := os.Lstat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("receipt: failed to stat receipt file %s: %w", filePath, err)
	}

	if fi.Mode()&os.ModeSymlink != 0 {
		return false, nil, fmt.Errorf("receipt: symlink detected for receipt file %s", filePath)
	}

	if fi.Size() > MaxReceiptFileSize {
		return false, nil, fmt.Errorf("receipt: receipt file %s exceeds maximum size limit (%d bytes)", filePath, fi.Size())
	}

	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("receipt: failed to open receipt file %s: %w", filePath, err)
	}
	defer file.Close()

	var dto DTO
	dec := json.NewDecoder(io.LimitReader(file, MaxReceiptFileSize+1))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&dto); err != nil {
		return false, nil, fmt.Errorf("receipt: corrupt receipt record in %s: %w", filePath, err)
	}

	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return false, nil, fmt.Errorf("receipt: trailing data in receipt file %s", filePath)
	}

	receipt, err := ConvertFromDTO(dto)
	if err != nil {
		return false, nil, fmt.Errorf("receipt: invalid cached receipt structure in %s: %w", filePath, err)
	}

	if err := receipt.Validate(); err != nil {
		return false, nil, fmt.Errorf("receipt: cached receipt validation failed in %s: %w", filePath, err)
	}

	if receipt.IdempotencyKey != op.IdempotencyKey {
		return false, nil, nil
	}

	// Found matching key: verify exact match or collision
	if receipt.Actor != op.Actor.EffectiveActor || receipt.Target != op.Target || receipt.Fingerprint != fp {
		return false, nil, ErrIdempotencyCollision
	}

	return true, &receipt, nil
}
