package sessions_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/sessions"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

func TestReconcileCrashedSessions(t *testing.T) {
	tempDir := t.TempDir()
	sd, err := statedir.Resolve(tempDir)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatalf("ensure dirs failed: %v", err)
	}

	// 1. Empty dir string and non-existent dir
	reconciled, err := sessions.ReconcileCrashedSessions(context.Background(), "", time.Time{})
	if err != nil || len(reconciled) != 0 {
		t.Errorf("expected empty reconciliation on empty dir string")
	}
	reconciled, err = sessions.ReconcileCrashedSessions(context.Background(), filepath.Join(tempDir, "non-existent"), time.Time{})
	if err != nil || len(reconciled) != 0 {
		t.Errorf("expected empty reconciliation on non-existent dir")
	}

	// 2. Create an active session file and an already terminal session file
	sessID := domain.SessionID("sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4")
	obs := domain.SessionObservation{
		ID:              sessID,
		Target:          "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		OwnerActor:      "agent:mcp-local",
		State:           domain.SessionStateActive,
		CreatedAt:       time.Now().UTC().Add(-5 * time.Minute),
		LastActivityAt:  time.Now().UTC().Add(-5 * time.Minute),
		Cols:            80,
		Rows:            24,
		TermType:        "xterm-256color",
		ObservationType: domain.ObservationObserved,
	}

	data, _ := json.MarshalIndent(obs, "", "  ")
	_ = os.WriteFile(filepath.Join(sd.SessionsDir(), string(sessID)+".json"), data, 0600)

	closedID := domain.SessionID("sess-b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5")
	closedObs := obs
	closedObs.ID = closedID
	closedObs.State = domain.SessionStateClosed
	closedData, _ := json.MarshalIndent(closedObs, "", "  ")
	_ = os.WriteFile(filepath.Join(sd.SessionsDir(), string(closedID)+".json"), closedData, 0600)

	// Run reconciliation
	now := time.Now().UTC()
	reconciled, err = sessions.ReconcileCrashedSessions(context.Background(), sd.SessionsDir(), now)
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if len(reconciled) != 1 || reconciled[0] != sessID {
		t.Fatalf("expected reconciled [%s], got %v", sessID, reconciled)
	}

	// Verify file was updated to crashed
	updatedData, err := os.ReadFile(filepath.Join(sd.SessionsDir(), string(sessID)+".json"))
	if err != nil {
		t.Fatalf("failed to read updated file: %v", err)
	}
	var updatedObs domain.SessionObservation
	if err := json.Unmarshal(updatedData, &updatedObs); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if updatedObs.State != domain.SessionStateCrashed {
		t.Errorf("expected state %s, got %s", domain.SessionStateCrashed, updatedObs.State)
	}
	if updatedObs.ErrorMessage != "daemon_crash_recovered" {
		t.Errorf("expected error message 'daemon_crash_recovered', got %q", updatedObs.ErrorMessage)
	}

	// Corrupted file test
	_ = os.WriteFile(filepath.Join(sd.SessionsDir(), "sess-corrupt.json"), []byte("invalid json"), 0600)
	_, err = sessions.ReconcileCrashedSessions(context.Background(), sd.SessionsDir(), now)
	if err == nil {
		t.Errorf("expected error on malformed session file")
	}
}
