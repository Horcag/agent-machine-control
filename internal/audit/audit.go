package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	// ErrTerminalEvidenceInvalid indicates finalized replay cannot prove its terminal audit record.
	ErrTerminalEvidenceInvalid = errors.New("audit: terminal evidence is missing or invalid")
)

// EventType identifies the lifecycle phase of an audit event.
type EventType string

const (
	EventAdmissionIntent EventType = "admission_intent"
	EventTerminalOutcome EventType = "terminal_outcome"
)

// Event represents an append-only audit event record.
type Event struct {
	SchemaVersion          string                `json:"schema_version"`
	Timestamp              time.Time             `json:"timestamp"`
	EventType              EventType             `json:"event_type"`
	Actor                  string                `json:"actor,omitempty"`
	Target                 string                `json:"target,omitempty"`
	OperationKind          string                `json:"operation_kind,omitempty"`
	Fingerprint            string                `json:"fingerprint,omitempty"`
	IdempotencyFingerprint string                `json:"idempotency_fingerprint,omitempty"`
	IdempotencyKey         string                `json:"idempotency_key,omitempty"`
	Classification         domain.OperationClass `json:"classification,omitempty"`
	ReceiptID              string                `json:"receipt_id,omitempty"`
	OutcomeStatus          domain.OutcomeStatus  `json:"outcome_status,omitempty"`
	ExitCode               int                   `json:"exit_code,omitempty"`
	ErrorCategory          string                `json:"error_category,omitempty"`
	ErrorMessage           string                `json:"error_message,omitempty"`
	RollbackRef            string                `json:"rollback_ref,omitempty"`
}

// Option configures Store behavior.
type Option func(*Store)

// WithAppendHook injects a pre-append failure hook for deterministic durability tests.
func WithAppendHook(fn func(Event) error) Option {
	return func(s *Store) { s.appendHook = fn }
}

// WithEnsureHook injects a context-aware pre-append boundary for deterministic deadline tests.
func WithEnsureHook(fn func(context.Context, Event) error) Option {
	return func(s *Store) { s.ensureHook = fn }
}

// WithPostAppendHook injects a boundary after append and before durable file sync.
func WithPostAppendHook(fn func()) Option {
	return func(s *Store) { s.postAppendHook = fn }
}

// WithWritableHook injects a context-aware writability boundary for deterministic platform tests.
func WithWritableHook(fn func(context.Context) error) Option {
	return func(s *Store) { s.writableHook = fn }
}

// WithRemoveFunc injects lock cleanup removal for deterministic failure tests.
func WithRemoveFunc(fn func(string) error) Option {
	return func(s *Store) { s.removeFn = fn }
}

// Store manages append-only persistence of audit events.
type Store struct {
	dir              string
	mu               sync.Mutex
	nowFn            func() time.Time
	syncDirFn        func(dir string) error
	closeFn          func(*os.File) error
	livenessChecker  lease.LivenessChecker
	identityProvider lease.IdentityProvider
	lockTimeout      time.Duration
	appendHook       func(Event) error
	ensureHook       func(context.Context, Event) error
	postAppendHook   func()
	writableHook     func(context.Context) error
	removeFn         func(string) error
}

// NewStore creates a new audit Store for the given directory.
func NewStore(dir string, opts ...Option) *Store {
	s := &Store{
		dir:              dir,
		nowFn:            time.Now,
		syncDirFn:        statedir.SyncDir,
		closeFn:          (*os.File).Close,
		livenessChecker:  &lease.DefaultLivenessChecker{},
		identityProvider: &lease.DefaultIdentityProvider{},
		lockTimeout:      5 * time.Second,
		removeFn:         os.Remove,
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
	return s.CheckWritableContext(context.Background())
}

// CheckWritableContext verifies writability within the caller's deadline.
func (s *Store) CheckWritableContext(ctx context.Context) error {
	if s.writableHook != nil {
		if err := s.writableHook(ctx); err != nil {
			return err
		}
	}
	if err := lockAuditStoreContext(ctx, &s.mu); err != nil {
		return err
	}
	defer s.mu.Unlock()

	return s.withLockContext(ctx, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
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
	return s.RecordAdmissionIntentContext(context.Background(), op)
}

// RecordAdmissionIntentContext writes an admission intent within the caller's deadline.
func (s *Store) RecordAdmissionIntentContext(ctx context.Context, op domain.Operation) error {
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

	return s.appendEventContext(ctx, event)
}

// RecordTerminalOutcome writes a terminal outcome audit event.
func (s *Store) RecordTerminalOutcome(r domain.Receipt) error {
	return s.appendEvent(terminalOutcomeEvent(r))
}

func terminalOutcomeEvent(r domain.Receipt) Event {
	return Event{
		SchemaVersion:          SchemaVersion,
		Timestamp:              r.CompletedAt.UTC(),
		EventType:              EventTerminalOutcome,
		Actor:                  string(r.Actor),
		Target:                 string(r.Target),
		OperationKind:          string(r.OperationKind),
		Fingerprint:            string(r.Fingerprint),
		IdempotencyFingerprint: string(r.IdempotencyFingerprint),
		IdempotencyKey:         r.IdempotencyKey,
		Classification:         r.Class,
		ReceiptID:              string(r.ReceiptID),
		OutcomeStatus:          r.Outcome.Status,
		ExitCode:               r.Outcome.ExitCode,
		ErrorCategory:          r.Outcome.ErrorCategory,
		ErrorMessage:           r.Outcome.ErrorMessage,
		RollbackRef:            r.RollbackRef,
	}
}

// EnsureTerminalOutcome idempotently appends the exact terminal event or rejects a collision.
func (s *Store) EnsureTerminalOutcome(r domain.Receipt) error {
	return s.EnsureTerminalOutcomeContext(context.Background(), r)
}

// EnsureTerminalOutcomeContext ensures exact terminal evidence within the caller's deadline.
func (s *Store) EnsureTerminalOutcomeContext(ctx context.Context, r domain.Receipt) error {
	if s == nil {
		return ErrAuditUnavailable
	}
	if err := r.Validate(); err != nil {
		return fmt.Errorf("audit: invalid terminal receipt: %w", err)
	}
	event := terminalOutcomeEvent(r)
	if err := lockAuditStoreContext(ctx, &s.mu); err != nil {
		return err
	}
	defer s.mu.Unlock()
	return s.withLockContext(ctx, func() error {
		return s.ensureTerminalOutcomeLocked(ctx, r, event)
	})
}

func (s *Store) ensureTerminalOutcomeLocked(ctx context.Context, receipt domain.Receipt, event Event) error {
	events, err := s.readEventsLockedContext(ctx)
	if err != nil {
		return err
	}
	found, err := findExactTerminalOutcome(events, receipt)
	if err != nil {
		return err
	}
	if found {
		if err := s.syncDirectory(); err != nil {
			return fmt.Errorf("%w: exact terminal event found but failed to sync directory %q: %w", ErrAuditUnavailable, s.dir, err)
		}
		return nil
	}
	if s.ensureHook != nil {
		if err := s.ensureHook(ctx, event); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.appendHook != nil {
		if err := s.appendHook(event); err != nil {
			return err
		}
	}
	return s.writeEventContext(ctx, event)
}

func findExactTerminalOutcome(events []Event, receipt domain.Receipt) (bool, error) {
	matched := 0
	for _, event := range events {
		if !validAuditEnvelope(event) {
			return false, fmt.Errorf("%w: invalid audit event envelope", ErrTerminalEvidenceInvalid)
		}
		if event.EventType != EventTerminalOutcome || event.ReceiptID != string(receipt.ReceiptID) {
			continue
		}
		if !terminalIdentityMatches(event, receipt) || !terminalOutcomeMatches(event, receipt) {
			return false, fmt.Errorf("%w: terminal receipt collision", ErrTerminalEvidenceInvalid)
		}
		matched++
	}
	if matched > 1 {
		return false, fmt.Errorf("%w: duplicate terminal receipt evidence", ErrTerminalEvidenceInvalid)
	}
	return matched == 1, nil
}

func (s *Store) appendEvent(event Event) error {
	return s.appendEventContext(context.Background(), event)
}

func (s *Store) appendEventContext(ctx context.Context, event Event) error {
	if err := lockAuditStoreContext(ctx, &s.mu); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if s.appendHook != nil {
		if err := s.appendHook(event); err != nil {
			return err
		}
	}

	return s.withLockContext(ctx, func() error { return s.writeEventContext(ctx, event) })
}

func (s *Store) writeEventContext(ctx context.Context, event Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("audit: failed to marshal event: %w", err)
	}
	data = append(data, '\n')
	f, err := os.OpenFile(s.logPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrAuditUnavailable, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("%w: %v", ErrAuditUnavailable, err)
	}
	if s.postAppendHook != nil {
		s.postAppendHook()
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("%w: audit event appended but file sync failed: %v", ErrAuditUnavailable, err)
	}
	closeFn := s.closeFn
	if closeFn == nil {
		closeFn = (*os.File).Close
	}
	if closeErr := closeFn(f); closeErr != nil {
		return fmt.Errorf("%w: audit event appended but file close failed: %w", ErrAuditUnavailable, closeErr)
	}
	if err := s.syncDirectory(); err != nil {
		return fmt.Errorf("%w: audit event appended but failed to sync directory %q: %v", ErrAuditUnavailable, s.dir, err)
	}
	return nil
}

func (s *Store) syncDirectory() error {
	syncFn := s.syncDirFn
	if syncFn == nil {
		syncFn = statedir.SyncDir
	}
	return syncFn(s.dir)
}
