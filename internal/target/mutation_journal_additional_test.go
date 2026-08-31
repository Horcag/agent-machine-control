package target

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestMutationJournalBindsDeadline(t *testing.T) {
	journal, _, record, _, deadline := newMutationJournalRecord(t, "journal-deadline")
	if !record.Deadline.Equal(deadline) {
		t.Fatalf("deadline = %s, want %s", record.Deadline, deadline)
	}
	if records, err := journal.ListContext(context.Background()); err != nil || len(records) != 1 {
		t.Fatalf("ListContext = %v, %v", records, err)
	}
}

func TestMutationJournalUpgradesLegacyRecordOnExactRetry(t *testing.T) {
	journal, op, record, now, deadline := newMutationJournalRecord(t, "journal-legacy")
	record.SchemaVersion = legacyMutationSchemaVersion
	record.Deadline = time.Time{}
	payload, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal.pathFor(op), payload, 0600); err != nil {
		t.Fatal(err)
	}
	if records, err := journal.ListContext(context.Background()); err != nil || len(records) != 1 || records[0].SchemaVersion != legacyMutationSchemaVersion {
		t.Fatalf("legacy ListContext = %v, %v", records, err)
	}
	upgraded, err := journal.ReserveContext(context.Background(), op, StateDigest(nil), StateDigest(nil), StateDigest(nil), 0, now)
	if err != nil || upgraded.SchemaVersion != mutationSchemaVersion || !upgraded.Deadline.Equal(deadline) {
		t.Fatalf("legacy upgrade = %+v, %v", upgraded, err)
	}
}

func TestMutationJournalRejectsUnknownEntries(t *testing.T) {
	dir := t.TempDir()
	journal, err := NewMutationJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, mutationDirName, "unexpected"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.ListContext(context.Background()); err != ErrMutationFinalization {
		t.Fatalf("ListContext unknown entry error = %v", err)
	}
}

func newMutationJournalRecord(t *testing.T, key string) (*MutationJournal, domain.Operation, *MutationRecord, time.Time, time.Time) {
	t.Helper()
	dir := t.TempDir()
	journal, err := NewMutationJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 31, 7, 0, 0, 0, time.UTC)
	deadline := now.Add(time.Minute)
	op := mutationTestOperation(mutationTestActor(t), key, deadline)
	record, err := journal.ReserveContext(context.Background(), op, StateDigest(nil), StateDigest(nil), StateDigest(nil), 0, now)
	if err != nil {
		t.Fatal(err)
	}
	return journal, op, record, now, deadline
}

func TestMutationJournalListsRecordsInCausalOrder(t *testing.T) {
	journal, err := NewMutationJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	actor := mutationTestActor(t)
	now := time.Date(2026, 8, 31, 7, 30, 0, 0, time.UTC)
	firstCandidate := mutationTestOperation(actor, "journal-order-a", now.Add(time.Minute))
	secondCandidate := mutationTestOperation(actor, "journal-order-b", now.Add(time.Minute))
	early, late := firstCandidate, secondCandidate
	if journal.pathFor(early) < journal.pathFor(late) {
		early, late = late, early
	}
	if _, err := journal.ReserveContext(context.Background(), early, StateDigest(nil), StateDigest(nil), StateDigest(nil), 0, now); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.ReserveContext(context.Background(), late, StateDigest(nil), StateDigest(nil), StateDigest(nil), 0, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	records, err := journal.ListContext(context.Background())
	if err != nil || len(records) != 2 {
		t.Fatalf("ListContext = %v, %v", records, err)
	}
	if records[0].IdempotencyKey != early.IdempotencyKey || records[1].IdempotencyKey != late.IdempotencyKey {
		t.Fatalf("causal order = %s, %s; want %s, %s", records[0].IdempotencyKey, records[1].IdempotencyKey, early.IdempotencyKey, late.IdempotencyKey)
	}
}

func mutationTestActor(t *testing.T) domain.ActorContext {
	t.Helper()
	scopes := domain.NewScopeSet(domain.ScopeTargetAdmin)
	actor, err := domain.NewActorContext("operator:test", "operator:test", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	return actor
}

func mutationTestOperation(actor domain.ActorContext, key string, deadline time.Time) domain.Operation {
	return domain.Operation{
		Kind: "target.clear", Target: "local:c4a523d4-6b99-4d62-a5e2-4752c0f20001", Actor: actor,
		Reason: "clear synthetic authority", Deadline: deadline, IdempotencyKey: key,
		RequiredScopes: []string{domain.ScopeTargetAdmin}, Classification: domain.ClassDestructivePrivileged,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters: map[string]any{
			"transition_hash": StateDigest(nil), "prior_hash": StateDigest(nil),
			"desired_hash": StateDigest(nil), "alias_count": 0,
		},
	}
}
