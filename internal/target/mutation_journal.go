package target

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

const (
	legacyMutationSchemaVersion = 1
	mutationSchemaVersion       = 2
	mutationDirName             = "mutations"
	maxMutationBytes            = 64 * 1024
)

var (
	ErrMutationCollision    = errors.New("target: mutation reservation collision")
	ErrMutationNotFound     = errors.New("target: mutation reservation not found")
	ErrMutationDrift        = errors.New("target: mutation authority state drift")
	ErrMutationFinalization = errors.New("target: mutation finalization is pending")
)

// MutationState records the durable lifecycle of one target authority request.
type MutationState string

const (
	MutationPending    MutationState = "pending"
	MutationFinalizing MutationState = "finalizing"
	MutationFinalized  MutationState = "finalized"
)

// MutationRecord is the protected redacted pre-effect reservation and terminal effect truth.
type MutationRecord struct {
	SchemaVersion          int                  `json:"schema_version"`
	Kind                   domain.OperationKind `json:"kind"`
	Actor                  domain.ActorID       `json:"actor"`
	Target                 domain.MachineRef    `json:"target"`
	Fingerprint            domain.Fingerprint   `json:"fingerprint"`
	IdempotencyFingerprint domain.Fingerprint   `json:"idempotency_fingerprint"`
	IdempotencyKey         string               `json:"idempotency_key"`
	PriorHash              string               `json:"prior_hash"`
	DesiredHash            string               `json:"desired_hash"`
	TransitionHash         string               `json:"transition_hash"`
	AliasCount             int                  `json:"alias_count"`
	State                  MutationState        `json:"state"`
	EffectApplied          bool                 `json:"effect_applied"`
	Committed              bool                 `json:"committed"`
	Durable                bool                 `json:"durable"`
	Receipt                *domain.Receipt      `json:"receipt,omitempty"`
	CreatedAt              time.Time            `json:"created_at"`
	Deadline               time.Time            `json:"deadline"`
	FinalizedAt            *time.Time           `json:"finalized_at,omitempty"`
}

// MutationJournalHook injects deterministic failures at durable lifecycle boundaries.
type MutationJournalHook func(action string) error

// MutationJournalOption configures a target mutation journal.
type MutationJournalOption func(*MutationJournal)

// WithMutationJournalHook configures a fault-injection hook.
func WithMutationJournalHook(hook MutationJournalHook) MutationJournalOption {
	return func(journal *MutationJournal) { journal.hook = hook }
}

// WithMutationJournalSecurity configures protected path validation for tests.
func WithMutationJournalSecurity(security Security) MutationJournalOption {
	return func(journal *MutationJournal) {
		if security != nil {
			journal.security = security
		}
	}
}

// MutationJournal owns strict pre-effect reservations below the protected target directory.
type MutationJournal struct {
	mu       sync.Mutex
	dir      string
	security Security
	hook     MutationJournalHook
}

// NewMutationJournal creates the protected target mutation namespace.
func NewMutationJournal(targetDir string, options ...MutationJournalOption) (*MutationJournal, error) {
	if targetDir == "" || !filepath.IsAbs(targetDir) {
		return nil, errors.New("target: mutation journal directory must be absolute")
	}
	journal := &MutationJournal{dir: filepath.Join(filepath.Clean(targetDir), mutationDirName), security: newPlatformSecurity()}
	for _, option := range options {
		option(journal)
	}
	if _, err := createPlatformMutationJournalDirectory(context.Background(), journal.dir, journal.security); err != nil {
		return nil, fmt.Errorf("target: create mutation journal: %w", err)
	}
	if err := journal.security.ValidateDir(context.Background(), journal.dir); err != nil {
		return nil, fmt.Errorf("%w: mutation directory", ErrInsecureState)
	}
	return journal, nil
}

// CheckWritableContext verifies the reservation namespace before policy admission.
func (j *MutationJournal) CheckWritableContext(ctx context.Context) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := j.security.ValidateDir(ctx, j.dir); err != nil {
		return ErrInsecureState
	}
	probe, err := os.CreateTemp(j.dir, ".writable-*")
	if err != nil {
		return ErrMutationFinalization
	}
	path := probe.Name()
	_ = probe.Close()
	_ = os.Remove(path)
	return nil
}

// ReserveContext creates or returns the exact redacted pre-effect reservation.
func (j *MutationJournal) ReserveContext(ctx context.Context, op domain.Operation, priorHash, desiredHash, transitionHash string, aliasCount int, now time.Time) (*MutationRecord, error) {
	record, err := mutationRecordFor(op, priorHash, desiredHash, transitionHash, aliasCount, now)
	if err != nil {
		return nil, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	path := j.pathFor(op)
	existing, err := j.readContext(ctx, path)
	if err == nil {
		if canUpgradeLegacyMutation(*existing, record) {
			existing.SchemaVersion = mutationSchemaVersion
			existing.Deadline = record.Deadline
			if err := j.replaceContext(ctx, path, *existing); err != nil {
				return nil, err
			}
			return existing, nil
		}
		if !sameMutationIdentity(*existing, record) {
			return nil, ErrMutationCollision
		}
		return existing, nil
	}
	if !errors.Is(err, ErrMutationNotFound) {
		return nil, err
	}
	if err := j.runHook("reserve"); err != nil {
		return nil, err
	}
	if err := j.writeExclusive(ctx, path, record); err != nil {
		return nil, err
	}
	return &record, nil
}

// LookupContext reads and identity-checks one existing reservation.
func (j *MutationJournal) LookupContext(ctx context.Context, op domain.Operation) (*MutationRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	record, err := j.readContext(ctx, j.pathFor(op))
	if err != nil {
		return nil, err
	}
	fp, _ := op.Fingerprint()
	idFP, _ := domain.ComputeIdempotencyFingerprint(op)
	if record.Fingerprint != fp || record.IdempotencyFingerprint != idFP || record.Target != op.Target || record.Kind != op.Kind {
		return nil, ErrMutationCollision
	}
	return record, nil
}

// LookupKeyContext reads a reservation by actor and idempotency key before operation reconstruction.
func (j *MutationJournal) LookupKeyContext(ctx context.Context, actor domain.ActorID, idempotencyKey string) (*MutationRecord, error) {
	if err := actor.Validate(); err != nil {
		return nil, err
	}
	if err := domain.ValidateIdempotencyKey(idempotencyKey); err != nil {
		return nil, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	digest := sha256.Sum256([]byte(string(actor) + "\x00" + idempotencyKey))
	return j.readContext(ctx, filepath.Join(j.dir, hex.EncodeToString(digest[:])+".json"))
}

// ListContext returns every strictly validated reservation in deterministic order.
func (j *MutationJournal) ListContext(ctx context.Context) ([]MutationRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(j.dir)
	if err != nil {
		return nil, ErrMutationFinalization
	}
	records := make([]MutationRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !isMutationRecordName(entry.Name()) {
			return nil, ErrMutationFinalization
		}
		record, err := j.readContext(ctx, filepath.Join(j.dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		records = append(records, *record)
	}
	sort.Slice(records, func(i, k int) bool {
		if !records[i].CreatedAt.Equal(records[k].CreatedAt) {
			return records[i].CreatedAt.Before(records[k].CreatedAt)
		}
		if records[i].Actor != records[k].Actor {
			return records[i].Actor < records[k].Actor
		}
		return records[i].IdempotencyKey < records[k].IdempotencyKey
	})
	return records, nil
}

func mutationRecordFor(op domain.Operation, priorHash, desiredHash, transitionHash string, aliasCount int, now time.Time) (MutationRecord, error) {
	if err := op.Validate(); err != nil {
		return MutationRecord{}, err
	}
	if err := domain.ValidateOperationParameters(op.Kind, op.Parameters); err != nil {
		return MutationRecord{}, err
	}
	fp, err := op.Fingerprint()
	if err != nil {
		return MutationRecord{}, err
	}
	idFP, err := domain.ComputeIdempotencyFingerprint(op)
	if err != nil {
		return MutationRecord{}, err
	}
	return MutationRecord{
		SchemaVersion: mutationSchemaVersion, Kind: op.Kind, Actor: op.Actor.EffectiveActor, Target: op.Target,
		Fingerprint: fp, IdempotencyFingerprint: idFP, IdempotencyKey: op.IdempotencyKey,
		PriorHash: priorHash, DesiredHash: desiredHash, TransitionHash: transitionHash, AliasCount: aliasCount,
		State: MutationPending, CreatedAt: now.UTC(), Deadline: op.Deadline.UTC(),
	}, nil
}

func (j *MutationJournal) pathFor(op domain.Operation) string {
	return j.pathForIdentity(op.Actor.EffectiveActor, op.IdempotencyKey)
}

func (j *MutationJournal) pathForIdentity(actor domain.ActorID, idempotencyKey string) string {
	digest := sha256.Sum256([]byte(string(actor) + "\x00" + idempotencyKey))
	return filepath.Join(j.dir, hex.EncodeToString(digest[:])+".json")
}

func isMutationRecordName(name string) bool {
	if len(name) != 69 || name[64:] != ".json" {
		return false
	}
	_, err := hex.DecodeString(name[:64])
	return err == nil
}

func sameMutationIdentity(left, right MutationRecord) bool {
	return left.Kind == right.Kind && left.Actor == right.Actor && left.Target == right.Target &&
		left.Fingerprint == right.Fingerprint && left.IdempotencyFingerprint == right.IdempotencyFingerprint &&
		left.IdempotencyKey == right.IdempotencyKey && left.PriorHash == right.PriorHash &&
		left.DesiredHash == right.DesiredHash && left.TransitionHash == right.TransitionHash && left.AliasCount == right.AliasCount &&
		left.Deadline.Equal(right.Deadline)
}

func canUpgradeLegacyMutation(existing, requested MutationRecord) bool {
	if existing.SchemaVersion != legacyMutationSchemaVersion || !existing.Deadline.IsZero() || requested.Deadline.IsZero() {
		return false
	}
	existing.SchemaVersion = requested.SchemaVersion
	existing.Deadline = requested.Deadline
	return sameMutationIdentity(existing, requested)
}

func recordMatchesOperation(record MutationRecord, op domain.Operation) bool {
	fp, err := op.Fingerprint()
	if err != nil {
		return false
	}
	idFP, err := domain.ComputeIdempotencyFingerprint(op)
	return err == nil && record.Fingerprint == fp && record.IdempotencyFingerprint == idFP && record.Target == op.Target && record.Kind == op.Kind
}

func (j *MutationJournal) readContext(ctx context.Context, path string) (*MutationRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := j.security.ValidateFile(ctx, path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrMutationNotFound
		}
		return nil, ErrInsecureState
	}
	file, err := openNoFollow(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrMutationNotFound
	}
	if err != nil {
		return nil, ErrMutationFinalization
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, maxMutationBytes+1))
	if err != nil || len(payload) > maxMutationBytes {
		return nil, ErrMutationFinalization
	}
	if err := rejectDuplicateMutationFields(payload); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var record MutationRecord
	if err := decoder.Decode(&record); err != nil {
		return nil, ErrMutationFinalization
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, ErrMutationFinalization
	}
	if err := validateMutationRecord(record); err != nil {
		return nil, err
	}
	return &record, nil
}

func validateMutationRecord(record MutationRecord) error {
	if !supportedMutationSchema(record) || record.CreatedAt.IsZero() ||
		(record.State != MutationPending && record.State != MutationFinalizing && record.State != MutationFinalized) {
		return ErrMutationFinalization
	}
	if record.Fingerprint.Validate() != nil || record.IdempotencyFingerprint.Validate() != nil {
		return ErrMutationFinalization
	}
	if !validMutationEffectState(record) {
		return ErrMutationFinalization
	}
	if record.Receipt != nil && record.Receipt.Validate() != nil {
		return ErrMutationFinalization
	}
	return nil
}

func supportedMutationSchema(record MutationRecord) bool {
	if record.SchemaVersion == mutationSchemaVersion {
		return !record.Deadline.IsZero()
	}
	return record.SchemaVersion == legacyMutationSchemaVersion && record.Deadline.IsZero()
}

func validMutationEffectState(record MutationRecord) bool {
	if record.State == MutationPending {
		return !record.EffectApplied && !record.Committed && record.Receipt == nil
	}
	return record.EffectApplied && record.Committed && record.Receipt != nil
}

func rejectDuplicateMutationFields(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return ErrMutationFinalization
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		field, err := decoder.Token()
		name, ok := field.(string)
		if err != nil || !ok {
			return ErrMutationFinalization
		}
		if _, duplicate := seen[name]; duplicate {
			return ErrMutationFinalization
		}
		seen[name] = struct{}{}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return ErrMutationFinalization
		}
	}
	return nil
}

func (j *MutationJournal) writeExclusive(ctx context.Context, path string, record MutationRecord) error {
	payload, err := json.Marshal(record)
	if err != nil {
		return ErrMutationFinalization
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return ErrMutationFinalization
	}
	committed := false
	defer func() {
		if file != nil {
			_ = file.Close()
		}
		if !committed {
			_ = os.Remove(path)
		}
	}()
	file, err = protectInheritedFileForWrite(ctx, file, path, j.security)
	if err != nil {
		return ErrInsecureState
	}
	if _, err := file.Write(payload); err != nil {
		return ErrMutationFinalization
	}
	if err := file.Sync(); err != nil {
		return ErrMutationFinalization
	}
	if err := file.Close(); err != nil {
		return ErrMutationFinalization
	}
	file = nil
	if err := statedir.SyncDir(j.dir); err != nil {
		return ErrMutationFinalization
	}
	committed = true
	return nil
}

func (j *MutationJournal) replaceContext(ctx context.Context, path string, record MutationRecord) error {
	payload, err := json.Marshal(record)
	if err != nil {
		return ErrMutationFinalization
	}
	temporary, err := os.CreateTemp(j.dir, ".mutation-*.tmp")
	if err != nil {
		return ErrMutationFinalization
	}
	temporaryPath := temporary.Name()
	defer func() {
		if temporary != nil {
			_ = temporary.Close()
		}
		_ = os.Remove(temporaryPath)
	}()
	temporary, err = protectInheritedFileForWrite(ctx, temporary, temporaryPath, j.security)
	if err != nil {
		return ErrInsecureState
	}
	if _, err := temporary.Write(payload); err != nil {
		return ErrMutationFinalization
	}
	if err := temporary.Sync(); err != nil {
		return ErrMutationFinalization
	}
	if err := temporary.Close(); err != nil {
		return ErrMutationFinalization
	}
	temporary = nil
	if err := os.Rename(temporaryPath, path); err != nil {
		return ErrMutationFinalization
	}
	if err := statedir.SyncDir(j.dir); err != nil {
		return ErrMutationFinalization
	}
	return nil
}

func (j *MutationJournal) runHook(action string) error {
	if j.hook == nil {
		return nil
	}
	return j.hook(action)
}
