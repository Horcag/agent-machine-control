package sessions_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/sessions"
)

func TestReconcileCrashedSessionsEmptyLocations(t *testing.T) {
	for _, dir := range []string{"", filepath.Join(t.TempDir(), "missing")} {
		reconciled, err := sessions.ReconcileCrashedSessions(context.Background(), dir, time.Time{})
		if err != nil || len(reconciled) != 0 {
			t.Fatalf("reconcile %q = %v, %v; want empty success", dir, reconciled, err)
		}
	}
}

func TestReconcileCrashedSessionsAtomicallyPersistsRestartTruth(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	obs := testSessionObservation("sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4", domain.SessionStateActive)
	path := writeSessionObservation(t, dir, obs)

	reconciled, err := sessions.ReconcileCrashedSessions(context.Background(), dir, now)
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if len(reconciled) != 1 || reconciled[0] != obs.ID {
		t.Fatalf("reconciled = %v, want [%s]", reconciled, obs.ID)
	}
	assertNoSessionTempFiles(t, dir)
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("reconciled file mode = %v, %v; want 0600", info, err)
	}

	actor := testSessionActor(t, obs.OwnerActor)
	for restart := range 3 {
		reconciled, err = sessions.ReconcileCrashedSessions(context.Background(), dir, now.Add(time.Duration(restart+1)*time.Minute))
		if err != nil || len(reconciled) != 0 {
			t.Fatalf("restart %d reconciliation = %v, %v; want terminal no-op", restart, reconciled, err)
		}
		loaded, err := sessions.NewManager(dir, nil, time.Now).Get(context.Background(), obs.ID, actor)
		if err != nil {
			t.Fatalf("restart %d read-back failed: %v", restart, err)
		}
		if loaded.State != domain.SessionStateCrashed || loaded.ClosedAt == nil || !loaded.ClosedAt.Equal(now) || loaded.ErrorMessage != "daemon_crash_recovered" {
			t.Fatalf("restart %d read-back = %+v, want stable crashed truth", restart, loaded)
		}
		assertNoSessionTempFiles(t, dir)
	}
}

func TestReconcileCrashedSessionsLeavesTerminalRecordUntouched(t *testing.T) {
	dir := t.TempDir()
	obs := testSessionObservation("sess-b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5", domain.SessionStateClosed)
	path := writeSessionObservation(t, dir, obs)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	reconciled, err := sessions.ReconcileCrashedSessions(context.Background(), dir, time.Now().UTC())
	if err != nil || len(reconciled) != 0 {
		t.Fatalf("terminal reconciliation = %v, %v; want no-op", reconciled, err)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, original) {
		t.Fatal("terminal session record was rewritten")
	}
	assertNoSessionTempFiles(t, dir)
}

func TestReconcileCrashedSessionsMalformedInputFailsClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess-corrupt.json")
	original := []byte("invalid json")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}

	if _, err := sessions.ReconcileCrashedSessions(context.Background(), dir, time.Now().UTC()); err == nil {
		t.Fatal("malformed session file unexpectedly reconciled")
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, original) {
		t.Fatal("malformed session record was modified")
	}
	assertNoSessionTempFiles(t, dir)
}

func testSessionObservation(id domain.SessionID, state domain.SessionState) domain.SessionObservation {
	createdAt := time.Date(2026, time.August, 30, 11, 0, 0, 0, time.UTC)
	return domain.SessionObservation{
		ID:              id,
		Target:          "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		OwnerActor:      "agent:reconcile-test",
		State:           state,
		CreatedAt:       createdAt,
		LastActivityAt:  createdAt,
		Cols:            80,
		Rows:            24,
		TermType:        "xterm-256color",
		ObservationType: domain.ObservationObserved,
	}
}

func writeSessionObservation(t *testing.T, dir string, obs domain.SessionObservation) string {
	t.Helper()
	data, err := json.MarshalIndent(obs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, string(obs.ID)+".json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testSessionActor(t *testing.T, actor domain.ActorID) domain.ActorContext {
	t.Helper()
	scopes := domain.NewScopeSet(domain.ScopeSessionRead)
	ctx, err := domain.NewActorContext(actor, actor, scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func assertNoSessionTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".session-") {
			t.Fatalf("temporary reconciliation residue remains: %s", entry.Name())
		}
	}
}
