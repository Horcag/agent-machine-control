package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

const (
	// SchemaVersion is the canonical audit event schema version.
	SchemaVersion = "1"

	// AuditFileName is the log filename.
	AuditFileName = "audit.jsonl"
)

var (
	// ErrAuditUnavailable indicates the audit log is unwritable or storage failed.
	ErrAuditUnavailable = errors.New("audit: audit storage is unavailable or unwritable")
)

// EventType identifies the lifecycle phase of an audit event.
type EventType string

const (
	EventAdmissionIntent EventType = "admission_intent"
	EventTerminalOutcome EventType = "terminal_outcome"
)

// Event represents an append-only audit event record.
type Event struct {
	SchemaVersion  string               `json:"schema_version"`
	Timestamp      time.Time            `json:"timestamp"`
	EventType      EventType            `json:"event_type"`
	Actor          string               `json:"actor,omitempty"`
	Target         string               `json:"target,omitempty"`
	OperationKind  string               `json:"operation_kind,omitempty"`
	Fingerprint    string               `json:"fingerprint,omitempty"`
	IdempotencyKey string               `json:"idempotency_key,omitempty"`
	ReceiptID      string               `json:"receipt_id,omitempty"`
	OutcomeStatus  domain.OutcomeStatus `json:"outcome_status,omitempty"`
	ExitCode       int                  `json:"exit_code,omitempty"`
	ErrorCategory  string               `json:"error_category,omitempty"`
	ErrorMessage   string               `json:"error_message,omitempty"`
	RollbackRef    string               `json:"rollback_ref,omitempty"`
}

// Option configures Store behavior.
type Option func(*Store)

// WithAppendHook injects a pre-append failure hook for deterministic durability tests.
func WithAppendHook(fn func(Event) error) Option {
	return func(s *Store) { s.appendHook = fn }
}

// Store manages append-only persistence of audit events.
type Store struct {
	dir              string
	mu               sync.Mutex
	nowFn            func() time.Time
	syncDirFn        func(dir string) error
	livenessChecker  lease.LivenessChecker
	identityProvider lease.IdentityProvider
	lockTimeout      time.Duration
	appendHook       func(Event) error
}

// NewStore creates a new audit Store for the given directory.
func NewStore(dir string, opts ...Option) *Store {
	s := &Store{
		dir:              dir,
		nowFn:            time.Now,
		syncDirFn:        statedir.SyncDir,
		livenessChecker:  &lease.DefaultLivenessChecker{},
		identityProvider: &lease.DefaultIdentityProvider{},
		lockTimeout:      5 * time.Second,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// WithSyncDir configures a custom directory sync function.
func WithSyncDir(fn func(dir string) error) Option {
	return func(s *Store) {
		s.syncDirFn = fn
	}
}

// WithClock sets a custom clock function for the audit store.
func WithClock(fn func() time.Time) Option {
	return func(s *Store) {
		s.nowFn = fn
	}
}

// WithLockTimeout sets a custom lock timeout for the audit store.
func WithLockTimeout(d time.Duration) Option {
	return func(s *Store) {
		s.lockTimeout = d
	}
}

// WithLivenessChecker configures a custom liveness checker.
func WithLivenessChecker(checker lease.LivenessChecker) Option {
	return func(s *Store) {
		s.livenessChecker = checker
	}
}

// WithIdentityProvider configures a custom identity provider.
func WithIdentityProvider(provider lease.IdentityProvider) Option {
	return func(s *Store) {
		s.identityProvider = provider
	}
}

func (s *Store) now() time.Time {
	if s.nowFn != nil {
		return s.nowFn().UTC()
	}
	return time.Now().UTC()
}

func (s *Store) logPath() string {
	return filepath.Join(s.dir, AuditFileName)
}

// CheckWritable verifies that the audit log file and directory can be written to.
func (s *Store) CheckWritable() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.withLock(func() error {
		testFile := filepath.Join(s.dir, fmt.Sprintf(".write_test_%d", time.Now().UnixNano()))
		f, err := os.OpenFile(testFile, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrAuditUnavailable, err)
		}
		_ = f.Close()
		_ = os.Remove(testFile)
		return nil
	})
}

// RecordAdmissionIntent writes an admission intent audit event.
func (s *Store) RecordAdmissionIntent(op domain.Operation) error {
	fp, err := op.Fingerprint()
	if err != nil {
		return fmt.Errorf("audit: failed to compute fingerprint: %w", err)
	}

	event := Event{
		SchemaVersion:  SchemaVersion,
		Timestamp:      s.now(),
		EventType:      EventAdmissionIntent,
		Actor:          string(op.Actor.EffectiveActor),
		Target:         string(op.Target),
		OperationKind:  string(op.Kind),
		Fingerprint:    string(fp),
		IdempotencyKey: op.IdempotencyKey,
	}

	return s.appendEvent(event)
}

// RecordTerminalOutcome writes a terminal outcome audit event.
func (s *Store) RecordTerminalOutcome(r domain.Receipt) error {
	event := Event{
		SchemaVersion: SchemaVersion,
		Timestamp:     s.now(),
		EventType:     EventTerminalOutcome,
		ReceiptID:     string(r.ReceiptID),
		OutcomeStatus: r.Outcome.Status,
		ExitCode:      r.Outcome.ExitCode,
		ErrorCategory: r.Outcome.ErrorCategory,
		ErrorMessage:  r.Outcome.ErrorMessage,
		RollbackRef:   r.RollbackRef,
	}

	return s.appendEvent(event)
}

func (s *Store) appendEvent(event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appendHook != nil {
		if err := s.appendHook(event); err != nil {
			return err
		}
	}

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("audit: failed to marshal event: %w", err)
	}
	data = append(data, '\n')

	return s.withLock(func() error {
		f, err := os.OpenFile(s.logPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrAuditUnavailable, err)
		}
		defer f.Close()

		if _, err := f.Write(data); err != nil {
			return fmt.Errorf("%w: %v", ErrAuditUnavailable, err)
		}
		if err := f.Sync(); err != nil {
			return fmt.Errorf("%w: %v", ErrAuditUnavailable, err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("%w: %v", ErrAuditUnavailable, err)
		}
		syncFn := s.syncDirFn
		if syncFn == nil {
			syncFn = statedir.SyncDir
		}
		if err := syncFn(s.dir); err != nil {
			return fmt.Errorf("%w: failed to sync audit directory %q: %v", ErrAuditUnavailable, s.dir, err)
		}
		return nil
	})
}

func (s *Store) withLock(fn func() error) error {
	lockDir := filepath.Join(s.dir, ".audit.lock")
	ownerPath := filepath.Join(lockDir, "owner.json")
	runtimeID, pid, startTime := s.identityProvider.CurrentIdentity()
	now := s.now()

	timeout := s.lockTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		err := os.Mkdir(lockDir, 0700)
		if err == nil {
			if recErr := s.recordLockOwner(ownerPath, runtimeID, pid, startTime, now); recErr != nil {
				_ = os.Remove(ownerPath)
				_ = os.Remove(lockDir)
				return fmt.Errorf("%w: %v", ErrAuditUnavailable, recErr)
			}
			defer s.releaseLock(ownerPath, lockDir, runtimeID, pid, startTime, now)
			return fn()
		}
		if !os.IsExist(err) {
			return fmt.Errorf("%w: failed to create audit lock: %v", ErrAuditUnavailable, err)
		}

		if s.tryReclaimDeadLock(ownerPath, lockDir, runtimeID) {
			continue
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("%w: timeout acquiring audit lock", ErrAuditUnavailable)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (s *Store) recordLockOwner(ownerPath, runtimeID string, pid int, startTime string, now time.Time) error {
	ownerRec := lease.LockOwnerRecord{
		SchemaVersion:    SchemaVersion,
		RuntimeID:        runtimeID,
		PID:              pid,
		ProcessStartTime: startTime,
		AcquiredAt:       now,
	}
	data, err := json.MarshalIndent(ownerRec, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal audit lock owner: %w", err)
	}
	f, err := os.OpenFile(ownerPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("failed to open audit lock owner file: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("failed to write audit lock owner file: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("failed to sync audit lock owner file: %w", err)
	}
	return f.Close()
}

func (s *Store) tryReclaimDeadLock(ownerPath, lockDir, runtimeID string) bool {
	ownerData, err := os.ReadFile(ownerPath)
	if err != nil {
		return false
	}
	var ownerRec lease.LockOwnerRecord
	dec := json.NewDecoder(bytes.NewReader(ownerData))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ownerRec); err != nil {
		return false
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return false
	}
	if ownerRec.SchemaVersion != SchemaVersion || ownerRec.RuntimeID != runtimeID || ownerRec.PID <= 0 {
		return false
	}
	alive, checkErr := s.livenessChecker.IsAlive(ownerRec.PID, ownerRec.ProcessStartTime)
	if checkErr != nil || alive {
		return false
	}
	_ = os.Remove(ownerPath)
	_ = os.Remove(lockDir)
	return true
}

func (s *Store) releaseLock(ownerPath, lockDir, runtimeID string, pid int, startTime string, acquiredAt time.Time) {
	ownerData, err := os.ReadFile(ownerPath)
	if err != nil {
		return
	}
	var ownerRec lease.LockOwnerRecord
	dec := json.NewDecoder(bytes.NewReader(ownerData))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ownerRec); err != nil {
		return
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return
	}
	if ownerRec.SchemaVersion == SchemaVersion &&
		ownerRec.RuntimeID == runtimeID &&
		ownerRec.PID == pid &&
		ownerRec.ProcessStartTime == startTime &&
		ownerRec.AcquiredAt.Equal(acquiredAt) {
		_ = os.Remove(ownerPath)
		_ = os.Remove(lockDir)
	}
}
