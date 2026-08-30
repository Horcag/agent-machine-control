package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestSessionStatePathResolvesCanonicalDirectChild(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	validID := domain.SessionID("sess-0123456789abcdef0123456789abcdef")
	got, err := sessionStatePath(root, validID)
	if err != nil {
		t.Fatalf("sessionStatePath() error = %v", err)
	}
	want := filepath.Join(root, string(validID)+".json")
	if got != want {
		t.Fatalf("sessionStatePath() = %q, want %q", got, want)
	}
	rel, err := filepath.Rel(root, got)
	if err != nil || filepath.IsAbs(rel) || filepath.Dir(rel) != "." {
		t.Fatalf("resolved path is not a direct child: rel=%q err=%v", rel, err)
	}
}

func TestSessionStatePathRejectsNonCanonicalIDs(t *testing.T) {
	t.Parallel()

	for _, id := range []domain.SessionID{
		"",
		"../outside",
		`..\outside`,
		`../..\outside`,
		"sess-0123456789abcdef0123456789abcdeF",
	} {
		t.Run(string(id), func(t *testing.T) {
			t.Parallel()
			if _, err := sessionStatePath(t.TempDir(), id); !errors.Is(err, domain.ErrInvalidSessionID) {
				t.Fatalf("sessionStatePath(%q) error = %v, want ErrInvalidSessionID", id, err)
			}
		})
	}
}

func TestManagerRejectsInvalidIDsBeforeStateAccess(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions")
	if err := os.Mkdir(sessionsDir, 0700); err != nil {
		t.Fatal(err)
	}
	owner := domain.ActorID("agent:containment-test")
	obs := domain.SessionObservation{
		ID:              "sess-0123456789abcdef0123456789abcdef",
		Target:          "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		OwnerActor:      owner,
		State:           domain.SessionStateClosed,
		CreatedAt:       time.Now().UTC(),
		LastActivityAt:  time.Now().UTC(),
		ObservationType: domain.ObservationObserved,
	}
	data, err := json.Marshal(obs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "outside.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	scopes := domain.NewScopeSet(
		domain.ScopeSessionRead,
		domain.ScopeSessionWrite,
		domain.ScopeSessionClose,
		domain.ScopeSessionAdmin,
	)
	caller, err := domain.NewActorContext(owner, owner, scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(sessionsDir, nil, time.Now)
	ctx := context.Background()

	for _, id := range []domain.SessionID{"../outside", `..\outside`, `../..\outside`} {
		t.Run(string(id), func(t *testing.T) {
			assertInvalidSessionID(t, func() error { _, err := mgr.Get(ctx, id, caller); return err })
			assertInvalidSessionID(t, func() error { _, _, _, _, _, err := mgr.Read(ctx, id, caller, 0, 1024); return err })
			assertInvalidSessionID(t, func() error {
				_, _, _, _, _, err := mgr.Wait(ctx, id, caller, time.Millisecond, "", 0, time.Millisecond)
				return err
			})
			assertInvalidSessionID(t, func() error { _, err := mgr.Write(ctx, id, caller, "x", "reason", "key"); return err })
			assertInvalidSessionID(t, func() error { return mgr.Control(ctx, id, caller, domain.ControlKeyCtrlC, "reason", "key") })
			assertInvalidSessionID(t, func() error { _, err := mgr.Close(ctx, id, caller, "reason", false); return err })
			assertInvalidSessionID(t, func() error { _, err := mgr.MutationTarget(id, caller); return err })
			assertInvalidSessionID(t, func() error { _, _, err := mgr.loadDiskSession(id, caller); return err })
		})
	}
}

func TestManagerDiskEnumerationSkipsInvalidFilenameID(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	owner := domain.ActorID("agent:enumeration-test")
	obs := domain.SessionObservation{
		ID:              "sess-0123456789abcdef0123456789abcdef",
		Target:          "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		OwnerActor:      owner,
		State:           domain.SessionStateClosed,
		CreatedAt:       time.Now().UTC(),
		LastActivityAt:  time.Now().UTC(),
		ObservationType: domain.ObservationObserved,
	}
	data, err := json.Marshal(obs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sess-invalid.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	scopes := domain.NewScopeSet(domain.ScopeSessionRead)
	caller, err := domain.NewActorContext(owner, owner, scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}

	got, err := NewManager(dir, nil, time.Now).List(context.Background(), caller, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("List() returned invalid-filename record: %+v", got)
	}
}

func assertInvalidSessionID(t *testing.T, call func() error) {
	t.Helper()
	if err := call(); !errors.Is(err, domain.ErrInvalidSessionID) {
		t.Fatalf("error = %v, want ErrInvalidSessionID", err)
	}
}
