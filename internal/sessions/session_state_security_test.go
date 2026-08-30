package sessions_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/sessions"
)

func TestCanonicalSessionStateCorruptionFailsClosed(t *testing.T) {
	owner := domain.ActorID("agent:state-boundary")
	actor := testSessionActor(t, owner)
	id := domain.SessionID("sess-0123456789abcdef0123456789abcdef")

	tests := []struct {
		name  string
		write func(*testing.T, string)
	}{
		{name: "mismatched payload identity", write: func(t *testing.T, dir string) {
			obs := testSessionObservation("sess-fedcba9876543210fedcba9876543210", domain.SessionStateActive)
			obs.OwnerActor = owner
			data, _ := json.Marshal(obs)
			if err := os.WriteFile(filepath.Join(dir, string(id)+".json"), data, 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "semantically invalid payload", write: func(t *testing.T, dir string) {
			obs := testSessionObservation(id, domain.SessionStateActive)
			obs.OwnerActor = owner
			obs.TermType = "xterm 256"
			data, _ := json.Marshal(obs)
			if err := os.WriteFile(filepath.Join(dir, string(id)+".json"), data, 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "oversized payload", write: func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, string(id)+".json"), []byte(strings.Repeat("x", (1<<20)+1)), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "canonical directory", write: func(t *testing.T, dir string) {
			if err := os.Mkdir(filepath.Join(dir, string(id)+".json"), 0700); err != nil {
				t.Fatal(err)
			}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.write(t, dir)
			mgr := sessions.NewManager(dir, nil, time.Now)
			if _, err := mgr.Get(context.Background(), id, actor); err == nil {
				t.Fatal("Get silently accepted canonical corruption")
			}
			if _, err := mgr.List(context.Background(), actor, ""); err == nil {
				t.Fatal("List silently accepted canonical corruption")
			}
			if _, err := sessions.ReconcileCrashedSessions(context.Background(), dir, time.Now().UTC()); err == nil {
				t.Fatal("reconciliation silently accepted canonical corruption")
			}
		})
	}
}

func TestCanonicalSessionSymlinkNeverFollowsOutsideRecord(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux/Unix symlink overlay; Windows helper is compile-checked separately")
	}
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.json")
	id := domain.SessionID("sess-0123456789abcdef0123456789abcdef")
	obs := testSessionObservation(id, domain.SessionStateActive)
	data, _ := json.Marshal(obs)
	if err := os.WriteFile(outside, data, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, string(id)+".json")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	actor := testSessionActor(t, obs.OwnerActor)
	mgr := sessions.NewManager(dir, nil, time.Now)
	if _, err := mgr.Get(context.Background(), id, actor); err == nil {
		t.Fatal("Get followed canonical symlink")
	}
	if _, err := mgr.List(context.Background(), actor, ""); err == nil {
		t.Fatal("List followed canonical symlink")
	}
	if _, err := sessions.ReconcileCrashedSessions(context.Background(), dir, time.Now().UTC()); err == nil {
		t.Fatal("reconcile followed canonical symlink")
	}
	current, err := os.ReadFile(outside)
	if err != nil || string(current) != string(data) {
		t.Fatalf("outside record changed: err=%v", err)
	}
}

func TestListValidatesCanonicalDiskRecordEvenWhenSessionIsLive(t *testing.T) {
	dir := t.TempDir()
	actor := deadlineGuardActor(t)
	channel := newDeadlineGuardChannel()
	mgr := sessions.NewManager(dir, &deadlineGuardTransport{channel: channel}, time.Now)
	obs, err := mgr.Open(context.Background(), deadlineGuardOperation(actor, "live-corrupt-list"), 80, 24, "xterm")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, string(obs.ID)+".json"), []byte("malformed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.List(context.Background(), actor, ""); err == nil {
		t.Fatal("List hid canonical disk corruption behind the live session")
	}
	if _, err := mgr.Write(context.Background(), obs.ID, actor, "x", "corruption persistence regression", "live-corrupt-write"); err == nil {
		t.Fatal("persistence overwrote canonical corruption")
	}
	current, err := os.ReadFile(filepath.Join(dir, string(obs.ID)+".json"))
	if err != nil || string(current) != "malformed" {
		t.Fatalf("canonical corruption changed: %q err=%v", current, err)
	}
	if err := mgr.Shutdown(context.Background()); err == nil {
		t.Fatal("shutdown persistence silently overwrote canonical corruption")
	}
}
