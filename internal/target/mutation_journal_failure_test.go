package target

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

type mutationJournalTestSecurity struct {
	dirErr     error
	fileErr    error
	protectErr error
}

type recordingMutationJournalSecurity struct {
	mutationJournalTestSecurity
	calls []string
}

func (s *recordingMutationJournalSecurity) ValidateDir(context.Context, string) error {
	s.calls = append(s.calls, "validate")
	return s.dirErr
}

func (s *recordingMutationJournalSecurity) ProtectDir(context.Context, string) error {
	s.calls = append(s.calls, "protect")
	return s.protectErr
}

func (s *mutationJournalTestSecurity) ValidateDir(context.Context, string) error {
	return s.dirErr
}

func (s *mutationJournalTestSecurity) ProtectDir(context.Context, string) error {
	return s.protectErr
}

func (s *mutationJournalTestSecurity) ValidateInheritedFile(context.Context, string) error {
	return s.fileErr
}

func (s *mutationJournalTestSecurity) ValidateFile(context.Context, string) error {
	return s.fileErr
}

func (s *mutationJournalTestSecurity) ProtectFile(context.Context, string) error {
	return s.protectErr
}

func TestMutationJournalFailsClosedOnSecurityAndCancellation(t *testing.T) {
	synthetic := errors.New("synthetic protected-path failure")
	if _, err := NewMutationJournal(t.TempDir(), WithMutationJournalSecurity(&mutationJournalTestSecurity{dirErr: synthetic})); !errors.Is(err, ErrInsecureState) {
		t.Fatalf("NewMutationJournal security error = %v", err)
	}
	if _, err := NewMutationJournal("relative"); err == nil {
		t.Fatal("relative mutation journal path unexpectedly accepted")
	}

	security := &mutationJournalTestSecurity{}
	journal, err := NewMutationJournal(t.TempDir(), WithMutationJournalSecurity(security))
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := journal.CheckWritableContext(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("CheckWritableContext cancellation = %v", err)
	}
	if _, err := journal.ListContext(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListContext cancellation = %v", err)
	}
	security.dirErr = synthetic
	if err := journal.CheckWritableContext(context.Background()); !errors.Is(err, ErrInsecureState) {
		t.Fatalf("CheckWritableContext security error = %v", err)
	}
	security.dirErr = nil

	now := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	op := mutationTestOperation(mutationTestActor(t), "journal-security", now.Add(time.Minute))
	security.protectErr = synthetic
	if _, err := journal.ReserveContext(context.Background(), op, StateDigest(nil), StateDigest(nil), StateDigest(nil), 0, now); !errors.Is(err, ErrInsecureState) {
		t.Fatalf("ReserveContext protection error = %v", err)
	}
	security.protectErr = nil
	record, err := journal.ReserveContext(context.Background(), op, StateDigest(nil), StateDigest(nil), StateDigest(nil), 0, now)
	if err != nil {
		t.Fatal(err)
	}
	security.fileErr = synthetic
	if _, err := journal.LookupContext(context.Background(), op); !errors.Is(err, ErrInsecureState) {
		t.Fatalf("LookupContext security error = %v", err)
	}
	security.fileErr = nil
	if _, err := journal.LookupContext(canceled, op); !errors.Is(err, context.Canceled) {
		t.Fatalf("LookupContext cancellation = %v", err)
	}
	if _, err := journal.LookupKeyContext(context.Background(), "", record.IdempotencyKey); err == nil {
		t.Fatal("empty actor unexpectedly accepted")
	}
	if _, err := journal.LookupKeyContext(context.Background(), record.Actor, "bad key"); err == nil {
		t.Fatal("invalid idempotency key unexpectedly accepted")
	}
}

func TestMutationJournalProtectsOnlyNewDirectoryBeforeValidation(t *testing.T) {
	t.Run("new directory", func(t *testing.T) {
		security := &recordingMutationJournalSecurity{}
		if _, err := NewMutationJournal(t.TempDir(), WithMutationJournalSecurity(security)); err != nil {
			t.Fatal(err)
		}
		if got, want := strings.Join(security.calls, ","), "protect,validate"; got != want {
			t.Fatalf("security calls = %q, want %q", got, want)
		}
	})

	t.Run("protection failure prevents validation", func(t *testing.T) {
		security := &recordingMutationJournalSecurity{mutationJournalTestSecurity: mutationJournalTestSecurity{protectErr: errors.New("cannot protect")}}
		if _, err := NewMutationJournal(t.TempDir(), WithMutationJournalSecurity(security)); !errors.Is(err, ErrInsecureState) {
			t.Fatalf("NewMutationJournal protection error = %v", err)
		}
		if got, want := strings.Join(security.calls, ","), "protect"; got != want {
			t.Fatalf("security calls = %q, want %q", got, want)
		}
	})

	t.Run("validation failure after protection is rejected", func(t *testing.T) {
		security := &recordingMutationJournalSecurity{mutationJournalTestSecurity: mutationJournalTestSecurity{dirErr: errors.New("invalid protection proof")}}
		if _, err := NewMutationJournal(t.TempDir(), WithMutationJournalSecurity(security)); !errors.Is(err, ErrInsecureState) {
			t.Fatalf("NewMutationJournal validation error = %v", err)
		}
		if got, want := strings.Join(security.calls, ","), "protect,validate"; got != want {
			t.Fatalf("security calls = %q, want %q", got, want)
		}
	})

	t.Run("insecure existing directory is not blessed", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, mutationDirName), 0700); err != nil {
			t.Fatal(err)
		}
		synthetic := errors.New("insecure pre-existing directory")
		security := &recordingMutationJournalSecurity{mutationJournalTestSecurity: mutationJournalTestSecurity{dirErr: synthetic}}
		if _, err := NewMutationJournal(root, WithMutationJournalSecurity(security)); !errors.Is(err, ErrInsecureState) {
			t.Fatalf("NewMutationJournal existing directory error = %v", err)
		}
		if got, want := strings.Join(security.calls, ","), "validate"; got != want {
			t.Fatalf("security calls = %q, want %q", got, want)
		}
	})
}

func TestMutationJournalRejectsMalformedProtectedRecords(t *testing.T) {
	journal, op, record, _, _ := newMutationJournalRecord(t, "journal-strict-record")
	path := journal.pathFor(op)
	valid, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(valid, &document); err != nil {
		t.Fatal(err)
	}
	document["unknown"] = true
	unknown, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"duplicate": []byte(`{"schema_version":2,"schema_version":2}`),
		"unknown":   unknown,
		"trailing":  append(append([]byte(nil), valid...), []byte(`{}`)...),
		"oversized": []byte(strings.Repeat("x", maxMutationBytes+1)),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, payload, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := journal.LookupKeyContext(context.Background(), record.Actor, record.IdempotencyKey); !errors.Is(err, ErrMutationFinalization) {
				t.Fatalf("LookupKeyContext malformed record error = %v", err)
			}
			//nolint:gosec // The fixture path is derived from the journal's private t.TempDir root.
			if err := os.WriteFile(path, valid, 0600); err != nil {
				t.Fatal(err)
			}
		})
	}

	invalid := *record
	invalid.State = "unknown"
	if !errors.Is(validateMutationRecord(invalid), ErrMutationFinalization) {
		t.Fatal("unknown mutation state unexpectedly accepted")
	}
	invalid = *record
	invalid.Fingerprint = "invalid"
	if !errors.Is(validateMutationRecord(invalid), ErrMutationFinalization) {
		t.Fatal("invalid fingerprint unexpectedly accepted")
	}
	invalid = *record
	invalid.EffectApplied = true
	if !errors.Is(validateMutationRecord(invalid), ErrMutationFinalization) {
		t.Fatal("invalid pending effect state unexpectedly accepted")
	}
}

func TestMutationJournalRejectsMismatchedEffectTransitions(t *testing.T) {
	journal, op, record, now, _ := newMutationJournalRecord(t, "journal-effect-denials")
	if _, err := journal.ReserveContext(context.Background(), op, "different-prior", record.DesiredHash, record.TransitionHash, record.AliasCount, now); !errors.Is(err, ErrMutationCollision) {
		t.Fatalf("reservation collision error = %v", err)
	}
	changedOperation := op
	changedOperation.Reason = "different target authority reason"
	if _, err := journal.LookupContext(context.Background(), changedOperation); !errors.Is(err, ErrMutationCollision) {
		t.Fatalf("operation collision error = %v", err)
	}
	if err := journal.MarkFinalizedContext(context.Background(), op, now); !errors.Is(err, ErrMutationFinalization) {
		t.Fatalf("premature finalize error = %v", err)
	}
	receipt := mutationEffectReceipt(*record, now.Add(time.Second))
	if err := journal.RecordEffectContext(context.Background(), op, receipt, false, false); !errors.Is(err, ErrMutationFinalization) {
		t.Fatalf("uncommitted effect error = %v", err)
	}
	mismatched := receipt
	mismatched.Fingerprint = domain.Fingerprint("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err := journal.RecordEffectContext(context.Background(), op, mismatched, true, false); !errors.Is(err, ErrMutationCollision) {
		t.Fatalf("mismatched receipt error = %v", err)
	}
	if err := journal.RecordEffectContext(context.Background(), op, receipt, true, false); err != nil {
		t.Fatal(err)
	}
	if err := journal.RecordEffectContext(context.Background(), op, receipt, true, true); !errors.Is(err, ErrMutationCollision) {
		t.Fatalf("effect replay collision = %v", err)
	}
	if err := journal.CancelContext(context.Background(), op); !errors.Is(err, ErrMutationFinalization) {
		t.Fatalf("applied operation cancellation = %v", err)
	}
	changedRecord := *record
	changedRecord.DesiredHash = "different-desired"
	if err := journal.RecordEffectForRecordContext(context.Background(), changedRecord, receipt, true); !errors.Is(err, ErrMutationCollision) {
		t.Fatalf("record effect collision = %v", err)
	}
	changedRecord.Fingerprint = domain.Fingerprint("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if err := journal.MarkFinalizedForRecordContext(context.Background(), changedRecord, now.Add(2*time.Second)); !errors.Is(err, ErrMutationCollision) {
		t.Fatalf("record finalize collision = %v", err)
	}

	pendingJournal, pendingOp, pendingRecord, _, _ := newMutationJournalRecord(t, "journal-cancel-denial")
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := pendingJournal.CancelContext(canceled, pendingOp); !errors.Is(err, context.Canceled) {
		t.Fatalf("CancelContext cancellation = %v", err)
	}
	if err := pendingJournal.CancelRecordContext(canceled, *pendingRecord); !errors.Is(err, context.Canceled) {
		t.Fatalf("CancelRecordContext cancellation = %v", err)
	}
}
