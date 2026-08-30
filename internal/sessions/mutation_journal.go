package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

const mutationReservationSchemaVersion = 2

var (
	ErrMutationReservationCollision = errors.New("sessions: mutation reservation collision")
	ErrMutationFinalizationPending  = errors.New("sessions: mutation finalization is pending")
	ErrMutationEffectUnknown        = errors.New("sessions: mutation effect is unknown after interrupted finalization")
)

type MutationReservationState string

const (
	MutationReservationPending    MutationReservationState = "pending"
	MutationReservationFinalizing MutationReservationState = "finalizing"
	MutationReservationFinalized  MutationReservationState = "finalized"
)

// MutationResult is the minimal immutable response data needed for an exact retry.
type MutationResult struct {
	BytesWritten  int                        `json:"bytes_written,omitempty"`
	Observation   *domain.SessionObservation `json:"observation,omitempty"`
	EffectApplied *bool                      `json:"effect_applied,omitempty"`
}

// EffectTruth returns explicit effect truth for new records and only unambiguous
// derived truth for legacy records that predate effect_applied.
func (r MutationResult) EffectTruth(operationKind domain.OperationKind) (applied, known bool) {
	if r.EffectApplied != nil {
		return *r.EffectApplied, true
	}
	if r.BytesWritten > 0 {
		return true, true
	}
	if r.Observation == nil {
		return false, false
	}
	switch operationKind {
	case "session.open":
		return true, true
	case "session.close":
		return r.Observation.State.IsTerminal(), r.Observation.State.IsTerminal()
	default:
		return false, false
	}
}

// MutationReservation is a durable pre-effect idempotency marker.
type MutationReservation struct {
	SchemaVersion          int                      `json:"schema_version"`
	OperationKind          domain.OperationKind     `json:"operation_kind"`
	Actor                  domain.ActorID           `json:"actor"`
	Target                 domain.MachineRef        `json:"target"`
	Classification         domain.OperationClass    `json:"classification"`
	IdempotencyKey         string                   `json:"idempotency_key"`
	IdempotencyFingerprint domain.Fingerprint       `json:"idempotency_fingerprint"`
	Fingerprint            domain.Fingerprint       `json:"fingerprint"`
	State                  MutationReservationState `json:"state"`
	CreatedAt              time.Time                `json:"created_at"`
	FinalizationStartedAt  *time.Time               `json:"finalization_started_at,omitempty"`
	FinalizedAt            *time.Time               `json:"finalized_at,omitempty"`
	ReceiptID              domain.ReceiptID         `json:"receipt_id,omitempty"`
	Receipt                *domain.Receipt          `json:"receipt,omitempty"`
	Result                 MutationResult           `json:"result"`
}

// MutationJournalHook supports deterministic storage-failure tests.
type MutationJournalHook func(action string) error

type MutationJournalOption func(*MutationJournal)

// WithMutationJournalHook injects a hook before reserve, finalize, or cancel persistence.
func WithMutationJournalHook(hook MutationJournalHook) MutationJournalOption {
	return func(j *MutationJournal) { j.hook = hook }
}

// MutationJournal stores session-scoped mutation reservations beside durable session state.
type MutationJournal struct {
	dir         string
	hook        MutationJournalHook
	contextHook func(context.Context, string) error
	lookupHook  func(context.Context) error
}

// WithMutationJournalLookupHook injects a context-aware lookup boundary for deadline tests.
func WithMutationJournalLookupHook(hook func(context.Context) error) MutationJournalOption {
	return func(j *MutationJournal) { j.lookupHook = hook }
}

// WithMutationJournalContextHook injects a context-aware storage boundary for deadline tests.
func WithMutationJournalContextHook(hook func(context.Context, string) error) MutationJournalOption {
	return func(j *MutationJournal) { j.contextHook = hook }
}

// NewMutationJournal creates a durable session mutation journal.
func NewMutationJournal(dir string, opts ...MutationJournalOption) *MutationJournal {
	j := &MutationJournal{dir: dir}
	for _, opt := range opts {
		opt(j)
	}
	return j
}

func (j *MutationJournal) ensureDir() error {
	fi, err := os.Lstat(j.dir)
	if err == nil {
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			return errors.New("sessions: invalid mutation journal directory")
		}
		if runtime.GOOS != "windows" && fi.Mode().Perm() != 0700 {
			return os.Chmod(j.dir, 0700)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	return os.Mkdir(j.dir, 0700)
}

func reservationName(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:]) + ".json"
}

func (j *MutationJournal) pathFor(key string) string {
	return filepath.Join(j.dir, reservationName(key))
}

func reservationFor(op domain.Operation, now time.Time) (MutationReservation, error) {
	fp, err := op.Fingerprint()
	if err != nil {
		return MutationReservation{}, err
	}
	idFp, err := domain.ComputeIdempotencyFingerprint(op)
	if err != nil {
		return MutationReservation{}, err
	}
	return MutationReservation{
		SchemaVersion:          mutationReservationSchemaVersion,
		OperationKind:          op.Kind,
		Actor:                  op.Actor.EffectiveActor,
		Target:                 op.Target,
		Classification:         op.Classification,
		IdempotencyKey:         op.IdempotencyKey,
		IdempotencyFingerprint: idFp,
		Fingerprint:            fp,
		State:                  MutationReservationPending,
		CreatedAt:              now.UTC(),
	}, nil
}

func (j *MutationJournal) callHook(action string) error {
	if j.hook == nil {
		return nil
	}
	return j.hook(action)
}

func (j *MutationJournal) callHookContext(ctx context.Context, action string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := j.callHook(action); err != nil {
		return err
	}
	if j.contextHook != nil {
		if err := j.contextHook(ctx, action); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// Lookup finds an exact reservation or returns a collision without disclosing its contents.
func (j *MutationJournal) Lookup(op domain.Operation) (*MutationReservation, error) {
	return j.LookupContext(context.Background(), op)
}

// LookupContext finds an exact reservation while honoring the caller's deadline.
func (j *MutationJournal) LookupContext(ctx context.Context, op domain.Operation) (*MutationReservation, error) {
	if j == nil || j.dir == "" || op.IdempotencyKey == "" {
		return nil, nil
	}
	if err := j.runLookupHooks(ctx); err != nil {
		return nil, err
	}
	record, err := j.readContext(ctx, j.pathFor(op.IdempotencyKey))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := validateReservationLookup(op, *record); err != nil {
		return nil, err
	}
	return record, nil
}

func (j *MutationJournal) runLookupHooks(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := j.callHook("lookup"); err != nil {
		return err
	}
	if j.lookupHook != nil {
		if err := j.lookupHook(ctx); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func validateReservationLookup(op domain.Operation, record MutationReservation) error {
	expected, err := reservationFor(op, record.CreatedAt)
	if err != nil {
		return err
	}
	if record.IdempotencyKey != expected.IdempotencyKey || record.Actor != expected.Actor || record.Target != expected.Target ||
		record.OperationKind != expected.OperationKind || record.Classification != expected.Classification ||
		record.IdempotencyFingerprint != expected.IdempotencyFingerprint {
		return ErrMutationReservationCollision
	}
	return nil
}

// Reserve atomically creates a pending marker before any guest effect.
func (j *MutationJournal) Reserve(op domain.Operation, now time.Time) (*MutationReservation, error) {
	return j.ReserveContext(context.Background(), op, now)
}

// ReserveContext atomically creates a pending marker within the caller's deadline.
func (j *MutationJournal) ReserveContext(ctx context.Context, op domain.Operation, now time.Time) (*MutationReservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := j.callHookContext(ctx, "reserve"); err != nil {
		return nil, err
	}
	if err := j.ensureDir(); err != nil {
		return nil, err
	}
	if existing, err := j.LookupContext(ctx, op); err != nil {
		return nil, err
	} else if existing != nil {
		return nil, ErrMutationFinalizationPending
	}
	record, err := reservationFor(op, now)
	if err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, err
	}
	path := j.pathFor(op.IdempotencyKey)
	err = writeReservationExclusive(ctx, path, data)
	if os.IsExist(err) {
		return nil, ErrMutationFinalizationPending
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, errors.Join(err, j.removeCanceledReservation(path))
		}
		return nil, err
	}
	if err := statedir.SyncDir(j.dir); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, j.removeCanceledReservation(path))
	}
	return &record, nil
}

func (j *MutationJournal) removeCanceledReservation(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return statedir.SyncDir(j.dir)
}

func writeReservationExclusive(ctx context.Context, path string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return ctx.Err()
}

// Cancel removes a known pre-effect reservation. Failure leaves the marker fail-closed.
func (j *MutationJournal) Cancel(op domain.Operation) error {
	return j.CancelContext(context.Background(), op)
}

// CancelContext removes a known pre-effect reservation within the caller's deadline.
func (j *MutationJournal) CancelContext(ctx context.Context, op domain.Operation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := j.callHook("cancel"); err != nil {
		return err
	}
	record, err := j.LookupContext(ctx, op)
	if err != nil {
		return err
	}
	if record == nil {
		return nil
	}
	if record.State != MutationReservationPending {
		return errors.New("sessions: finalized mutation reservation cannot be cancelled")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(j.pathFor(op.IdempotencyKey)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return statedir.SyncDir(j.dir)
}

func (j *MutationJournal) replaceContext(ctx context.Context, path string, record MutationReservation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp.%d", path, time.Now().UnixNano())
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := ctx.Err(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return statedir.SyncDir(j.dir)
}

func (j *MutationJournal) readContext(ctx context.Context, path string) (*MutationReservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() || fi.Size() > 64*1024 {
		return nil, errors.New("sessions: invalid mutation reservation file")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	record, err := decodeMutationReservation(f)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateMutationReservation(record); err != nil {
		return nil, err
	}
	return &record, ctx.Err()
}

func decodeMutationReservation(r io.Reader) (MutationReservation, error) {
	var record MutationReservation
	dec := json.NewDecoder(io.LimitReader(r, 64*1024+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&record); err != nil {
		return MutationReservation{}, err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return MutationReservation{}, errors.New("sessions: trailing mutation reservation data")
	}
	return record, nil
}

func validateMutationReservation(record MutationReservation) error {
	if err := validateMutationReservationEnvelope(record); err != nil {
		return err
	}
	if err := validateMutationReservationReceipt(record); err != nil {
		return err
	}
	return validateMutationReservationState(record)
}

func validateMutationReservationEnvelope(record MutationReservation) error {
	validSchema := record.SchemaVersion == 1 || record.SchemaVersion == mutationReservationSchemaVersion
	validState := record.State == MutationReservationPending || record.State == MutationReservationFinalizing || record.State == MutationReservationFinalized
	if !validSchema || record.IdempotencyKey == "" || !record.Classification.IsValid() || !validState {
		return errors.New("sessions: invalid mutation reservation record")
	}
	return nil
}

func validateMutationReservationReceipt(record MutationReservation) error {
	if record.Receipt != nil {
		if err := record.Receipt.Validate(); err != nil || !receiptMatchesReservation(*record.Receipt, record) || record.ReceiptID != record.Receipt.ReceiptID {
			return errors.New("sessions: invalid mutation finalization receipt")
		}
	}
	return nil
}

func validateMutationReservationState(record MutationReservation) error {
	if record.State == MutationReservationFinalizing && (record.Receipt == nil || record.FinalizationStartedAt == nil || record.FinalizedAt != nil) {
		return errors.New("sessions: invalid finalizing mutation record")
	}
	if record.State == MutationReservationFinalized && record.SchemaVersion >= mutationReservationSchemaVersion && (record.Receipt == nil || record.FinalizationStartedAt == nil || record.FinalizedAt == nil) {
		return errors.New("sessions: invalid finalized mutation record")
	}
	return nil
}
