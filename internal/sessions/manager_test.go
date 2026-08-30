package sessions_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
	"github.com/Horcag/agent-machine-control/internal/guest/ssh/fakeserver"
	"github.com/Horcag/agent-machine-control/internal/sessions"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	gossh "golang.org/x/crypto/ssh"
)

func setupTestManager(t *testing.T) (*sessions.Manager, *fakeserver.FakeSSHServer, domain.ActorContext, domain.ActorContext, string) {
	tempDir := t.TempDir()
	sd, _ := statedir.Resolve(tempDir)
	_ = sd.EnsureDirs()

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := gossh.NewSignerFromKey(priv)
	sshPub, _ := gossh.NewPublicKey(pub)

	server, err := fakeserver.New(fakeserver.ModeEcho, sshPub)
	if err != nil {
		t.Fatalf("failed to create fake ssh server: %v", err)
	}

	kp := &guestssh.MockKeyProvider{
		Signer:          signer,
		PinnedKeySHA256: server.HostKeyPin(),
		Endpoint:        server.Addr(),
		User:            "testadmin",
	}

	transport := guestssh.NewTransport(kp)
	mgr := sessions.NewManager(sd.SessionsDir(), transport, time.Now)

	agentScopes := domain.NewScopeSet(domain.ScopeSessionRead, domain.ScopeSessionWrite, domain.ScopeSessionOpen, domain.ScopeSessionClose)
	agentActor, _ := domain.NewActorContext("agent:test", "agent:test", agentScopes, agentScopes)

	otherScopes := domain.NewScopeSet(domain.ScopeSessionRead, domain.ScopeSessionWrite)
	otherActor, _ := domain.NewActorContext("agent:other", "agent:other", otherScopes, otherScopes)

	return mgr, server, agentActor, otherActor, sd.SessionsDir()
}

func TestManagerMutationTargetJournalAndSanitizerOption(t *testing.T) {
	mgr, server, actor, other, sessionsDir := setupTestManager(t)
	defer server.Close()
	obs, _ := testManagerOpenAndRead(t, mgr, actor)
	target, err := mgr.MutationTarget(obs.ID, actor)
	if err != nil || target != obs.Target {
		t.Fatalf("active mutation target=%s err=%v", target, err)
	}
	if _, err := mgr.MutationTarget(obs.ID, other); !errors.Is(err, domain.ErrSessionNotFound) {
		t.Fatalf("cross-actor mutation target error=%v", err)
	}
	restarted := sessions.NewManager(sessionsDir, nil, time.Now, sessions.WithSanitizerConfig(guestssh.SanitizerConfig{ExactSecrets: [][]byte{[]byte("synthetic")}}))
	target, err = restarted.MutationTarget(obs.ID, actor)
	if err != nil || target != obs.Target {
		t.Fatalf("durable mutation target=%s err=%v", target, err)
	}
	journal := restarted.MutationJournal()
	if journal == nil {
		t.Fatal("manager did not expose its mutation journal")
	}
	if err := journal.CheckWritable(); err != nil {
		t.Fatal(err)
	}
	if sessions.NewManager("", nil, time.Now).MutationJournal() != nil {
		t.Fatal("manager without durable state exposed a journal")
	}
}

func testManagerOpenAndRead(t *testing.T, mgr *sessions.Manager, actorCtx domain.ActorContext) (*domain.SessionObservation, uint64) {
	ctx := context.Background()
	op := domain.Operation{
		Kind:                "session.open",
		Target:              "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Actor:               actorCtx,
		Reason:              "test lifecycle",
		Deadline:            time.Now().UTC().Add(1 * time.Minute),
		IdempotencyKey:      "idem-1",
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	obs, err := mgr.Open(ctx, op, 80, 24, "xterm-256color")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	if obs.State != domain.SessionStateActive {
		t.Errorf("expected active state, got %s", obs.State)
	}

	time.Sleep(50 * time.Millisecond)
	chunks, nextSeq, _, _, readObs, err := mgr.Read(ctx, obs.ID, actorCtx, 0, 1024)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if len(chunks) == 0 {
		t.Errorf("expected at least 1 chunk for prompt")
	}
	if readObs.ID != obs.ID {
		t.Errorf("expected matching observation ID")
	}
	return obs, nextSeq
}

func testManagerWriteAndControl(t *testing.T, mgr *sessions.Manager, actorCtx domain.ActorContext, obs *domain.SessionObservation, nextSeq uint64) {
	ctx := context.Background()
	wn, err := mgr.Write(ctx, obs.ID, actorCtx, "hostname\r\n", "run command", "key-write-1")
	if err != nil || wn == 0 {
		t.Fatalf("Write failed: %v", err)
	}

	if err := mgr.Control(ctx, obs.ID, actorCtx, domain.ControlKeyCtrlC, "interrupt", "key-ctrl-1"); err != nil {
		t.Fatalf("Control failed: %v", err)
	}

	settleChunks, _, _, _, _, err := mgr.Wait(ctx, obs.ID, actorCtx, 50*time.Millisecond, "", nextSeq, 2*time.Second)
	if err != nil {
		t.Fatalf("Wait failed: %v", err)
	}
	if len(settleChunks) == 0 {
		t.Logf("settle returned 0 chunks (already read)")
	}
}

func testManagerDenialAndClose(t *testing.T, mgr *sessions.Manager, actorCtx, otherActor domain.ActorContext, obs *domain.SessionObservation) {
	ctx := context.Background()
	_, _, _, _, _, err := mgr.Read(ctx, obs.ID, otherActor, 0, 1024)
	if err == nil || !errors.Is(err, domain.ErrSessionNotFound) {
		t.Errorf("expected ErrSessionNotFound for unauthorized actor, got %v", err)
	}

	getObs, err := mgr.Get(ctx, obs.ID, actorCtx)
	if err != nil || getObs.ID != obs.ID {
		t.Fatalf("Get failed: %v", err)
	}

	listObs, err := mgr.List(ctx, actorCtx, "")
	if err != nil || len(listObs) != 1 {
		t.Fatalf("List failed: got %d items, err %v", len(listObs), err)
	}

	closedObs, err := mgr.Close(ctx, obs.ID, actorCtx, "finished", false)
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if closedObs.State != domain.SessionStateClosed {
		t.Errorf("expected closed state, got %s", closedObs.State)
	}
}

func TestManager_FullLifecycle(t *testing.T) {
	mgr, server, actorCtx, otherActor, _ := setupTestManager(t)
	defer server.Close()

	obs, nextSeq := testManagerOpenAndRead(t, mgr, actorCtx)
	testManagerWriteAndControl(t, mgr, actorCtx, obs, nextSeq)
	testManagerDenialAndClose(t, mgr, actorCtx, otherActor, obs)
}

func TestManager_Idempotency(t *testing.T) {
	mgr, server, actorCtx, _, _ := setupTestManager(t)
	defer server.Close()
	ctx := context.Background()

	op := domain.Operation{
		Kind:                "session.open",
		Target:              "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Actor:               actorCtx,
		Reason:              "test idempotency",
		Deadline:            time.Now().UTC().Add(1 * time.Minute),
		IdempotencyKey:      "idem-open-1",
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	// First open
	obs1, err := mgr.Open(ctx, op, 80, 24, "xterm-256color")
	if err != nil {
		t.Fatalf("first open failed: %v", err)
	}

	// Exact retry with same parameters -> returns obs1
	obs2, err := mgr.Open(ctx, op, 80, 24, "xterm-256color")
	if err != nil {
		t.Fatalf("idempotent retry failed: %v", err)
	}
	if obs1.ID != obs2.ID {
		t.Errorf("expected same session ID %s, got %s", obs1.ID, obs2.ID)
	}

	// Conflict retry with same idempotency key but different parameters -> returns conflict
	_, err = mgr.Open(ctx, op, 120, 40, "xterm-256color")
	if err == nil || !errors.Is(err, domain.ErrSessionConflict) {
		t.Errorf("expected ErrSessionConflict, got %v", err)
	}
}

func TestManager_ConcurrentReadWriteCloseRaces(t *testing.T) {
	mgr, server, actorCtx, _, _ := setupTestManager(t)
	defer server.Close()
	ctx := context.Background()

	op := domain.Operation{
		Kind:                "session.open",
		Target:              "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Actor:               actorCtx,
		Reason:              "test race",
		Deadline:            time.Now().UTC().Add(1 * time.Minute),
		IdempotencyKey:      "idem-race-1",
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	obs, err := mgr.Open(ctx, op, 80, 24, "xterm-256color")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(3)

	// Goroutine 1: Continuous reads
	go func() {
		defer wg.Done()
		for range 50 {
			_, _, _, _, _, _ = mgr.Read(ctx, obs.ID, actorCtx, 0, 1024)
			time.Sleep(1 * time.Millisecond)
		}
	}()

	// Goroutine 2: Continuous writes
	go func() {
		defer wg.Done()
		for range 50 {
			_, _ = mgr.Write(ctx, obs.ID, actorCtx, "echo hi\r\n", "write", "k")
			time.Sleep(1 * time.Millisecond)
		}
	}()

	// Goroutine 3: Close after small delay
	go func() {
		defer wg.Done()
		time.Sleep(20 * time.Millisecond)
		_, _ = mgr.Close(ctx, obs.ID, actorCtx, "close race", false)
	}()

	wg.Wait()
}

func TestManager_NotFoundAndClosedErrors(t *testing.T) {
	mgr, server, actorCtx, _, _ := setupTestManager(t)
	defer server.Close()
	ctx := context.Background()

	badID := domain.SessionID("sess-00000000000000000000000000000000")

	// Non-existent operations return ErrSessionNotFound
	if _, err := mgr.Get(ctx, badID, actorCtx); !errors.Is(err, domain.ErrSessionNotFound) {
		t.Errorf("expected ErrSessionNotFound on Get, got: %v", err)
	}
	if _, _, _, _, _, err := mgr.Read(ctx, badID, actorCtx, 0, 1024); !errors.Is(err, domain.ErrSessionNotFound) {
		t.Errorf("expected ErrSessionNotFound on Read, got: %v", err)
	}
	if _, err := mgr.Write(ctx, badID, actorCtx, "data", "reason", "key"); !errors.Is(err, domain.ErrSessionNotFound) {
		t.Errorf("expected ErrSessionNotFound on Write, got: %v", err)
	}
	if err := mgr.Control(ctx, badID, actorCtx, domain.ControlKeyCtrlC, "reason", "key"); !errors.Is(err, domain.ErrSessionNotFound) {
		t.Errorf("expected ErrSessionNotFound on Control, got: %v", err)
	}
	if _, _, _, _, _, err := mgr.Wait(ctx, badID, actorCtx, 10*time.Millisecond, "", 0, 100*time.Millisecond); !errors.Is(err, domain.ErrSessionNotFound) {
		t.Errorf("expected ErrSessionNotFound on Wait, got: %v", err)
	}
	if _, err := mgr.Close(ctx, badID, actorCtx, "reason", false); !errors.Is(err, domain.ErrSessionNotFound) {
		t.Errorf("expected ErrSessionNotFound on Close, got: %v", err)
	}
}

func TestManager_AccessControl(t *testing.T) {
	mgr, server, actorCtx, otherActor, _ := setupTestManager(t)
	defer server.Close()
	ctx := context.Background()

	op := domain.Operation{
		Kind:                "session.open",
		Target:              "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Actor:               actorCtx,
		Reason:              "test access",
		Deadline:            time.Now().UTC().Add(time.Minute),
		IdempotencyKey:      "idem-access-1",
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	obs, err := mgr.Open(ctx, op, 80, 24, "xterm-256color")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	// Other actor without ownership gets ErrSessionNotFound
	if _, err := mgr.Get(ctx, obs.ID, otherActor); !errors.Is(err, domain.ErrSessionNotFound) {
		t.Errorf("expected ErrSessionNotFound for non-owner, got: %v", err)
	}

	// List with matching target and mismatch target
	list, err := mgr.List(ctx, actorCtx, "c4a523d4-6b99-4d62-a5e2-4752c0f20001")
	if err != nil || len(list) != 1 {
		t.Errorf("expected 1 session from List matching target, got: %v (err: %v)", list, err)
	}
	list, err = mgr.List(ctx, actorCtx, "c4a523d4-6b99-4d62-a5e2-4752c0f29999")
	if err != nil || len(list) != 0 {
		t.Errorf("expected 0 sessions from List mismatch target, got: %v", list)
	}

	// Scope denial on List
	noReadScopes := domain.NewScopeSet(domain.ScopeSessionWrite)
	noReadActor, _ := domain.NewActorContext("agent:test", "agent:test", noReadScopes, noReadScopes)
	if _, err := mgr.List(ctx, noReadActor, ""); !errors.Is(err, domain.ErrSessionAccessDenied) {
		t.Errorf("expected ErrSessionAccessDenied on List without read scope")
	}

	// Scope denial on Close
	noCloseScopes := domain.NewScopeSet(domain.ScopeSessionRead)
	noCloseActor, _ := domain.NewActorContext("agent:test", "agent:test", noCloseScopes, noCloseScopes)
	if _, err := mgr.Close(ctx, obs.ID, noCloseActor, "reason", false); !errors.Is(err, domain.ErrSessionAccessDenied) {
		t.Errorf("expected ErrSessionAccessDenied on Close without close scope")
	}
}

func TestManager_ShutdownAndPersistence(t *testing.T) {
	mgr, server, actorCtx, _, sessionsDir := setupTestManager(t)
	defer server.Close()
	ctx := context.Background()

	op := domain.Operation{
		Kind:                "session.open",
		Target:              "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Actor:               actorCtx,
		Reason:              "test shutdown",
		Deadline:            time.Now().UTC().Add(time.Minute),
		IdempotencyKey:      "idem-shutdown-1",
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	obs, err := mgr.Open(ctx, op, 80, 24, "xterm-256color")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	// Shutdown terminates all active sessions
	if err := mgr.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown failed: %v", err)
	}

	// Get after shutdown confirms closed state
	shutObs, err := mgr.Get(ctx, obs.ID, actorCtx)
	if err != nil || shutObs.State != domain.SessionStateClosed {
		t.Errorf("expected closed state after shutdown, got: %+v (err: %v)", shutObs, err)
	}

	// Create a new manager instance pointing to the same sessionsDir (simulating daemon restart)
	newMgr := sessions.NewManager(sessionsDir, nil, time.Now)
	loadedObs, err := newMgr.Get(ctx, obs.ID, actorCtx)
	if err != nil {
		t.Fatalf("failed to load session from disk after restart: %v", err)
	}
	if loadedObs.ID != obs.ID || loadedObs.State != domain.SessionStateClosed {
		t.Errorf("unexpected loaded session observation: %+v", loadedObs)
	}
	loadedList, err := newMgr.List(ctx, actorCtx, "")
	if err != nil || len(loadedList) != 1 {
		t.Errorf("failed to load session list from disk: len=%d, err=%v", len(loadedList), err)
	}
}

func TestManager_WaitRegexAndDiskLoadingFilters(t *testing.T) {
	mgr, server, actorCtx, _, sessionsDir := setupTestManager(t)
	defer server.Close()
	ctx := context.Background()

	op := domain.Operation{
		Kind:                "session.open",
		Target:              "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Actor:               actorCtx,
		Reason:              "test wait regex",
		Deadline:            time.Now().UTC().Add(time.Minute),
		IdempotencyKey:      "idem-regex-1",
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	obs, err := mgr.Open(ctx, op, 80, 24, "xterm-256color")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	// Write prompt trigger
	_, _ = mgr.Write(ctx, obs.ID, actorCtx, "echo test\r\n", "write", "k")

	// Wait with regex pattern
	chunks, nextSeq, _, matched, _, err := mgr.Wait(ctx, obs.ID, actorCtx, 0, "PS C:", 0, 2*time.Second)
	if err != nil || !matched || len(chunks) == 0 {
		t.Errorf("expected regex match on prompt, got: matched=%v, chunks=%d, err=%v", matched, len(chunks), err)
	}
	if nextSeq == 0 {
		t.Errorf("expected nextSeq > 0")
	}

	// Scope denial for write without write scope
	noWriteScopes := domain.NewScopeSet(domain.ScopeSessionRead)
	noWriteActor, _ := domain.NewActorContext("agent:test", "agent:test", noWriteScopes, noWriteScopes)
	if _, err := mgr.Write(ctx, obs.ID, noWriteActor, "data", "r", "k"); !errors.Is(err, domain.ErrSessionAccessDenied) {
		t.Errorf("expected ErrSessionAccessDenied on Write without write scope, got: %v", err)
	}
	if err := mgr.Control(ctx, obs.ID, noWriteActor, domain.ControlKeyCtrlC, "r", "k"); !errors.Is(err, domain.ErrSessionAccessDenied) {
		t.Errorf("expected ErrSessionAccessDenied on Control without write scope, got: %v", err)
	}

	// Create non-session file and corrupt file in sessionsDir to test disk loader resilience
	_ = os.WriteFile(filepath.Join(sessionsDir, "not-a-session.txt"), []byte("random text"), 0600)
	_ = os.WriteFile(filepath.Join(sessionsDir, "sess-corrupt.json"), []byte("{invalid json"), 0600)

	newMgr := sessions.NewManager(sessionsDir, nil, time.Now)
	list, err := newMgr.List(ctx, actorCtx, "c4a523d4-6b99-4d62-a5e2-4752c0f20001")
	if err != nil || len(list) != 1 {
		t.Errorf("expected 1 session loaded from disk ignoring corrupt files, got %d (err: %v)", len(list), err)
	}
}
