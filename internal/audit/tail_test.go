package audit_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestStore_Tail_EmptyAndNonExistent(t *testing.T) {
	dir := t.TempDir()
	store := audit.NewStore(dir)

	events, err := store.Tail(10)
	if err != nil {
		t.Fatalf("expected nil error on empty store, got: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("expected 0 events, got %d", len(events))
	}
}

func TestStore_Tail_BoundedOrder(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	store := audit.NewStore(dir, audit.WithClock(func() time.Time { return now }))

	for i := 1; i <= 10; i++ {
		op := domain.Operation{
			Kind:           "machine.start",
			Target:         "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
			Actor:          domain.ActorContext{AuthenticatedCaller: "test-actor", EffectiveActor: "test-actor"},
			Reason:         fmt.Sprintf("reason %d", i),
			Deadline:       now.Add(time.Hour),
			IdempotencyKey: fmt.Sprintf("key-%d", i),
			Classification: domain.ClassReversibleMutation,
		}
		if err := store.RecordAdmissionIntent(op); err != nil {
			t.Fatalf("failed to record admission %d: %v", i, err)
		}
	}

	// Tail last 3
	events, err := store.Tail(3)
	if err != nil {
		t.Fatalf("failed to tail: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	if events[0].IdempotencyKey != "key-8" || events[1].IdempotencyKey != "key-9" || events[2].IdempotencyKey != "key-10" {
		t.Errorf("unexpected events order: %+v", events)
	}
}

func TestStore_TailLimitBounds(t *testing.T) {
	dir := t.TempDir()
	store := audit.NewStore(dir)

	// Negative limit defaults to 50
	events, err := store.Tail(-5)
	if err != nil || len(events) != 0 {
		t.Errorf("expected empty list for negative limit, got events %v, err %v", events, err)
	}

	// Excessive limit capped to 1000
	events, err = store.Tail(2000)
	if err != nil || len(events) != 0 {
		t.Errorf("expected empty list for excessive limit, got events %v, err %v", events, err)
	}
}

func TestStore_TailCorruptLine(t *testing.T) {
	dir := t.TempDir()
	store := audit.NewStore(dir)

	// Write invalid JSON line to audit.jsonl
	_ = os.WriteFile(filepath.Join(dir, audit.AuditFileName), []byte("corrupt-line\n"), 0600)
	_, err := store.Tail(10)
	if err == nil {
		t.Errorf("expected error reading corrupt audit log")
	}
}

func TestStore_TailOversizedLine(t *testing.T) {
	dir := t.TempDir()
	store := audit.NewStore(dir)

	// Write oversized line (> 64KB)
	oversized := make([]byte, audit.MaxLineBytes+100)
	for i := range oversized {
		oversized[i] = 'a'
	}
	oversized[len(oversized)-1] = '\n'

	_ = os.WriteFile(filepath.Join(dir, audit.AuditFileName), oversized, 0600)
	_, err := store.Tail(10)
	if err == nil {
		t.Errorf("expected error reading oversized audit line")
	}
}
