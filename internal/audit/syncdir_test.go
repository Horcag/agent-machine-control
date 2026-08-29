package audit_test

import (
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestAuditStore_AppendEvent_SyncDirFailure(t *testing.T) {
	dir := t.TempDir()
	syncErr := errors.New("simulated audit directory fsync failure")
	store := audit.NewStore(dir, audit.WithSyncDir(func(_ string) error {
		return syncErr
	}))

	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	alice, _ := domain.NewActorContext("user:alice", "user:alice", domain.NewScopeSet("machine:write"), domain.NewScopeSet("machine:write"))
	op := domain.Operation{
		Kind:                "machine.start",
		Target:              "a0b1c2d3-e4f5-6789-abcd-ef0123456789",
		Actor:               alice,
		Reason:              "test sync fail",
		Deadline:            now.Add(5 * time.Minute),
		IdempotencyKey:      "key-sync-audit",
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	err := store.RecordAdmissionIntent(op)
	if err == nil {
		t.Fatalf("expected error when directory sync fails during audit append")
	}
	if !errors.Is(err, audit.ErrAuditUnavailable) {
		t.Errorf("expected ErrAuditUnavailable, got %v", err)
	}
}
