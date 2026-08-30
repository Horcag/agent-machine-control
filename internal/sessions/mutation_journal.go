package sessions

import (
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

const mutationReservationSchemaVersion = 1

var (
	ErrMutationReservationCollision = errors.New("sessions: mutation reservation collision")
	ErrMutationFinalizationPending  = errors.New("sessions: mutation finalization is pending")
)

type MutationReservationState string

const (
	MutationReservationPending   MutationReservationState = "pending"
	MutationReservationFinalized MutationReservationState = "finalized"
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
	FinalizedAt            *time.Time               `json:"finalized_at,omitempty"`
	ReceiptID              domain.ReceiptID         `json:"receipt_id,omitempty"`
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
	dir  string
	hook MutationJournalHook
}

// NewMutationJournal creates a durable session mutation journal.
func NewMutationJournal(dir string, opts ...MutationJournalOption) *MutationJournal {
	j := &MutationJournal{dir: dir}
	for _, opt := range opts {
		opt(j)
	}
	return j
}

// CheckWritable verifies that the journal can create and remove durable markers.
func (j *MutationJournal) CheckWritable() error {
	if j == nil || j.dir == "" {
		return errors.New("sessions: mutation journal is unavailable")
	}
	if err := j.ensureDir(); err != nil {
		return err
	}
	probe := filepath.Join(j.dir, fmt.Sprintf(".write-test-%d", time.Now().UnixNano()))
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
	return statedir.SyncDir(j.dir)
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

// Lookup finds an exact reservation or returns a collision without disclosing its contents.
func (j *MutationJournal) Lookup(op domain.Operation) (*MutationReservation, error) {
	if j == nil || j.dir == "" || op.IdempotencyKey == "" {
		return nil, nil
	}
	record, err := j.read(j.pathFor(op.IdempotencyKey))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	expected, err := reservationFor(op, record.CreatedAt)
	if err != nil {
		return nil, err
	}
	if record.Actor != expected.Actor || record.Target != expected.Target || record.OperationKind != expected.OperationKind || record.Classification != expected.Classification || record.IdempotencyFingerprint != expected.IdempotencyFingerprint {
		return nil, ErrMutationReservationCollision
	}
	return record, nil
}

// Reserve atomically creates a pending marker before any guest effect.
func (j *MutationJournal) Reserve(op domain.Operation, now time.Time) (*MutationReservation, error) {
	if err := j.callHook("reserve"); err != nil {
		return nil, err
	}
	if err := j.ensureDir(); err != nil {
		return nil, err
	}
	if existing, err := j.Lookup(op); err != nil {
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
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if os.IsExist(err) {
			return nil, ErrMutationFinalizationPending
		}
		return nil, err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	if err := statedir.SyncDir(j.dir); err != nil {
		return nil, err
	}
	return &record, nil
}

// Finalize marks a reservation complete only after receipt and audit persistence succeed.
func (j *MutationJournal) Finalize(op domain.Operation, receiptID domain.ReceiptID, result MutationResult, now time.Time) error {
	if err := j.callHook("finalize"); err != nil {
		return err
	}
	record, err := j.Lookup(op)
	if err != nil {
		return err
	}
	if record == nil {
		return errors.New("sessions: mutation reservation is missing")
	}
	finalizedAt := now.UTC()
	record.State = MutationReservationFinalized
	record.FinalizedAt = &finalizedAt
	record.ReceiptID = receiptID
	record.Result = result
	return j.replace(j.pathFor(op.IdempotencyKey), *record)
}

// Cancel removes a known pre-effect reservation. Failure leaves the marker fail-closed.
func (j *MutationJournal) Cancel(op domain.Operation) error {
	if err := j.callHook("cancel"); err != nil {
		return err
	}
	record, err := j.Lookup(op)
	if err != nil {
		return err
	}
	if record == nil {
		return nil
	}
	if record.State != MutationReservationPending {
		return errors.New("sessions: finalized mutation reservation cannot be cancelled")
	}
	if err := os.Remove(j.pathFor(op.IdempotencyKey)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return statedir.SyncDir(j.dir)
}

func (j *MutationJournal) replace(path string, record MutationReservation) error {
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
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return statedir.SyncDir(j.dir)
}

func (j *MutationJournal) read(path string) (*MutationReservation, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() || fi.Size() > 64*1024 {
		return nil, errors.New("sessions: invalid mutation reservation file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var record MutationReservation
	dec := json.NewDecoder(io.LimitReader(f, 64*1024+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&record); err != nil {
		return nil, err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, errors.New("sessions: trailing mutation reservation data")
	}
	if record.SchemaVersion != mutationReservationSchemaVersion || record.IdempotencyKey == "" || !record.Classification.IsValid() || (record.State != MutationReservationPending && record.State != MutationReservationFinalized) {
		return nil, errors.New("sessions: invalid mutation reservation record")
	}
	return &record, nil
}
