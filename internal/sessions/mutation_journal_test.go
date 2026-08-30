package sessions_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/sessions"
)

func journalOperation(t *testing.T, key string) domain.Operation {
	t.Helper()
	scopes := domain.NewScopeSet(domain.ScopeSessionWrite)
	actor, err := domain.NewActorContext("agent:journal", "agent:journal", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	return domain.Operation{
		Kind:                "session.write",
		Target:              "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Actor:               actor,
		Reason:              "verify durable reservation",
		Deadline:            time.Now().UTC().Add(time.Minute),
		IdempotencyKey:      key,
		RequiredCapability:  domain.CapabilitySessionWrite,
		RequiredScopes:      []string{domain.ScopeSessionWrite},
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters: map[string]any{
			"session_id":  "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4",
			"data_sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"data_length": 16,
		},
	}
}

func assertJournalCollisions(t *testing.T, journal *sessions.MutationJournal, op domain.Operation) {
	t.Helper()
	collisions := []domain.Operation{op.Clone(), op.Clone(), op.Clone(), op.Clone()}
	collisions[0].Actor.EffectiveActor = "agent:other"
	collisions[1].Target = "c4a523d4-6b99-4d62-a5e2-4752c0f20002"
	collisions[2].Kind = "session.control"
	collisions[2].RequiredCapability = domain.CapabilitySessionControl
	collisions[2].Parameters = map[string]any{
		"session_id": "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4",
		"key":        string(domain.ControlKeyCtrlC),
	}
	collisions[3].Parameters["data_length"] = 17
	for i, collision := range collisions {
		if _, err := journal.Lookup(collision); !errors.Is(err, sessions.ErrMutationReservationCollision) {
			t.Errorf("collision %d error=%v", i, err)
		}
	}
}

func TestMutationJournalLifecycleAndCollisionIsolation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mutations")
	journal := sessions.NewMutationJournal(dir)
	if err := journal.CheckWritable(); err != nil {
		t.Fatal(err)
	}
	op := journalOperation(t, "idem-journal-lifecycle")
	if record, err := journal.Lookup(op); err != nil || record != nil {
		t.Fatalf("initial lookup: record=%v err=%v", record, err)
	}
	now := time.Now().UTC()
	reserved, err := journal.Reserve(op, now)
	if err != nil || reserved.State != sessions.MutationReservationPending {
		t.Fatalf("reserve: record=%+v err=%v", reserved, err)
	}
	if _, err := journal.Reserve(op, now); !errors.Is(err, sessions.ErrMutationFinalizationPending) {
		t.Fatalf("duplicate reserve error=%v", err)
	}

	assertJournalCollisions(t, journal, op)

	receiptID, err := domain.GenerateReceiptID()
	if err != nil {
		t.Fatal(err)
	}
	observation := &domain.SessionObservation{ID: "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4", State: domain.SessionStateActive}
	if err := journal.Finalize(op, receiptID, sessions.MutationResult{BytesWritten: 16, Observation: observation}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	finalized, err := journal.Lookup(op)
	if err != nil || finalized.State != sessions.MutationReservationFinalized || finalized.ReceiptID != receiptID || finalized.Result.BytesWritten != 16 {
		t.Fatalf("finalized lookup: record=%+v err=%v", finalized, err)
	}
	if err := journal.Cancel(op); err == nil {
		t.Fatal("finalized reservation was cancelled")
	}
}

func TestMutationJournalCancelAndMissingReservation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mutations")
	op := journalOperation(t, "idem-journal-cancel")
	journal := sessions.NewMutationJournal(dir)
	if _, err := journal.Reserve(op, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := journal.Cancel(op); err != nil {
		t.Fatal(err)
	}
	if record, err := journal.Lookup(op); err != nil || record != nil {
		t.Fatalf("lookup after cancel: record=%v err=%v", record, err)
	}
	if err := journal.Cancel(op); err != nil {
		t.Fatalf("missing cancel should be idempotent: %v", err)
	}
	if err := journal.Finalize(op, "rcpt-0123456789abcdef0123456789abcdef", sessions.MutationResult{}, time.Now()); err == nil {
		t.Fatal("missing reservation finalized")
	}
}

func TestMutationResultLegacyEffectTruthIsOnlyDerivedWhenUnambiguous(t *testing.T) {
	active := &domain.SessionObservation{State: domain.SessionStateActive}
	terminal := &domain.SessionObservation{State: domain.SessionStateFailed}
	falseValue := false
	tests := []struct {
		name       string
		kind       domain.OperationKind
		result     sessions.MutationResult
		wantEffect bool
		wantKnown  bool
	}{
		{name: "explicit false", kind: "session.control", result: sessions.MutationResult{EffectApplied: &falseValue}, wantKnown: true},
		{name: "accepted bytes", kind: "session.control", result: sessions.MutationResult{BytesWritten: 1}, wantEffect: true, wantKnown: true},
		{name: "published open", kind: "session.open", result: sessions.MutationResult{Observation: active}, wantEffect: true, wantKnown: true},
		{name: "terminal close", kind: "session.close", result: sessions.MutationResult{Observation: terminal}, wantEffect: true, wantKnown: true},
		{name: "incomplete close", kind: "session.close", result: sessions.MutationResult{Observation: active}},
		{name: "ambiguous control", kind: "session.control", result: sessions.MutationResult{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotEffect, gotKnown := tt.result.EffectTruth(tt.kind)
			if gotEffect != tt.wantEffect || gotKnown != tt.wantKnown {
				t.Fatalf("EffectTruth() = (%v, %v), want (%v, %v)", gotEffect, gotKnown, tt.wantEffect, tt.wantKnown)
			}
		})
	}
}

func TestMutationJournalHooksFailClosed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "mutations")
	op := journalOperation(t, "idem-journal-hooks")
	hooked := sessions.NewMutationJournal(dir, sessions.WithMutationJournalHook(func(action string) error {
		return errors.New("synthetic " + action + " failure")
	}))
	if _, err := hooked.Reserve(op, time.Now()); err == nil {
		t.Fatal("reserve hook failure was ignored")
	}
	if err := hooked.Finalize(op, "rcpt-0123456789abcdef0123456789abcdef", sessions.MutationResult{}, time.Now()); err == nil {
		t.Fatal("finalize hook failure was ignored")
	}
	if err := hooked.Cancel(op); err == nil {
		t.Fatal("cancel hook failure was ignored")
	}
}

func TestMutationJournalCorruptAndInvalidDirectoryFailClosed(t *testing.T) {
	base := t.TempDir()
	badPath := filepath.Join(base, "not-a-directory")
	if err := os.WriteFile(badPath, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := sessions.NewMutationJournal(badPath).CheckWritable(); err == nil {
		t.Fatal("file-backed journal directory was accepted")
	}

	dir := filepath.Join(base, "mutations")
	journal := sessions.NewMutationJournal(dir)
	corruptOp := journalOperation(t, "idem-journal-corrupt")
	if _, err := journal.Reserve(corruptOp, time.Now()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			if err := os.WriteFile(filepath.Join(dir, entry.Name()), []byte("{} trailing"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := journal.Lookup(corruptOp); err == nil {
		t.Fatal("corrupt reservation did not fail closed")
	}
}
