package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/sessions"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestPersistentVMGrantSurvivesElapsedTimeAndSafetyChanges(t *testing.T) {
	f := newConsoleFixture(t)
	safety, epoch := configureLab(f)
	grant, _, err := f.service.IssueLabGrant(context.Background(), f.actor, app.ConsoleLabGrantIssueRequest{Mode: app.ConsoleLabModePersistentVM, Target: "default", Reason: "operate enrolled test VM", IdempotencyKey: "persistent-vm", Beneficiary: "agent:mcp-local", AcknowledgeExternalEffects: true})
	if err != nil {
		t.Fatal(err)
	}
	if !grant.ExpiresAt.IsZero() {
		t.Fatal("persistent grant expires")
	}
	*f.now = f.now.Add(48 * time.Hour)
	safety.contained, safety.unverified = false, true
	req := labRequest(grant)
	req.Deadline = f.now.Add(time.Minute).Format(time.RFC3339Nano)
	if _, err := f.service.Input(context.Background(), labActor(t), req); err != nil {
		t.Fatal(err)
	}
	*epoch = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	req.IdempotencyKey = "after-reenrollment"
	if _, err := f.service.Input(context.Background(), labActor(t), req); err == nil || len(f.provider.inputs) != 1 {
		t.Fatalf("stale authority dispatched: %v", err)
	}
}

func TestPersistentVMGrantCoordinatesSessionsAndRejectsRevokedAuthority(t *testing.T) {
	f := newConsoleFixture(t)
	configureLab(f)
	grant, _, err := f.service.IssueLabGrant(t.Context(), f.actor, app.ConsoleLabGrantIssueRequest{Mode: app.ConsoleLabModePersistentVM, Target: "default", Reason: "operate enrolled test VM", IdempotencyKey: "persistent-session", Beneficiary: "agent:mcp-local", AcknowledgeExternalEffects: true})
	if err != nil {
		t.Fatal(err)
	}
	// A later bounded desktop grant cannot shadow VM authority for sessions.
	*f.now = f.now.Add(time.Millisecond)
	issueLab(t, f)
	scopes := domain.NewScopeSet(domain.ScopeSessionOpen, domain.ScopeSessionWrite, domain.ScopeSessionRead, domain.ScopeSessionClose)
	actor, err := domain.NewActorContext("agent:mcp-local", "agent:mcp-local", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	transport := &trackingTransport{}
	manager := sessions.NewManager(filepath.Join(f.root, "sessions"), transport, time.Now)
	coordinator := app.NewSessionService(manager, &dynamicClassSafetyResolver{resolution: destructiveSafetyResolution()}, nil, audit.NewStore(filepath.Join(f.root, "audit")), receipt.NewStore(filepath.Join(f.root, "receipts")), approval.NewStore(filepath.Join(f.root, "approvals")), app.WithSessionClock(func() time.Time { return *f.now }), app.WithSessionLabAuthority(f.service))
	params := app.SessionOpenParams{Target: string(grant.Target), Caller: actor, Reason: "open enrolled VM session", IdempotencyKey: "persistent-session-open", Timeout: time.Minute, Deadline: f.now.Add(time.Minute), Cols: 80, Rows: 24}
	obs, rcpt, err := coordinator.OpenSession(t.Context(), params)
	if err != nil || obs == nil || rcpt == nil || rcpt.Outcome.Status != domain.OutcomeSuccess {
		t.Fatalf("persistent session: %+v %+v %v", obs, rcpt, err)
	}
	t.Cleanup(func() {
		_, _, _ = coordinator.CloseSession(context.Background(), app.SessionCloseParams{SessionID: obs.ID, Caller: actor, Reason: "cleanup test session", IdempotencyKey: "cleanup-session", Timeout: time.Second})
	})
	if n, _, err := coordinator.WriteSession(t.Context(), app.SessionWriteParams{SessionID: obs.ID, Caller: actor, Data: "synthetic command", Reason: "exercise persistent write", IdempotencyKey: "persistent-write", Timeout: time.Minute}); err != nil || n == 0 {
		t.Fatalf("persistent write: %v", err)
	}
	if _, err := coordinator.ControlSession(t.Context(), app.SessionControlParams{SessionID: obs.ID, Caller: actor, Key: domain.ControlKeyCtrlC, Reason: "exercise persistent control", IdempotencyKey: "persistent-control", Timeout: time.Minute}); err != nil {
		t.Fatalf("persistent control: %v", err)
	}
	if _, _, err := coordinator.CloseSession(t.Context(), app.SessionCloseParams{SessionID: obs.ID, Caller: actor, Reason: "exercise persistent close", IdempotencyKey: "persistent-close", Timeout: time.Minute}); err != nil {
		t.Fatalf("persistent close: %v", err)
	}
	params.IdempotencyKey = "invalid-explicit"
	params.ApprovalID = "app-00000000000000000000000000000000"
	if _, _, err := coordinator.OpenSession(t.Context(), params); err == nil {
		t.Fatal("explicit bad approval fell back to grant")
	}
	params.ApprovalID = ""
	_, err = f.service.RevokeLabGrant(t.Context(), f.actor, app.ConsoleLabGrantRevokeRequest{GrantID: grant.GrantID, Reason: "end VM authority", IdempotencyKey: "revoke-session", Deadline: f.now.Add(time.Minute).Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	params.IdempotencyKey = "revoked-open"
	if _, _, err := coordinator.OpenSession(t.Context(), params); err == nil || atomic.LoadInt32(&transport.dialCalls) != 1 {
		t.Fatal("revoked session dispatched")
	}
}

func TestLabDerivedApprovalCannotEscapeRevocationAfterLeaseConflict(t *testing.T) {
	f := newConsoleFixture(t)
	configureLab(f)
	grant := issueLab(t, f)
	op, req, providerID := labOperation(t, grant)
	leases := lease.NewManager(filepath.Join(f.root, "leases"))
	held, err := leases.Acquire(t.Context(), providerID, "synthetic.blocker", "synthetic-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ExecuteLabMutation(t.Context(), req.Actor, grant.GrantID, op, req, providerID, func(context.Context) error { t.Error("busy provider dispatched"); return nil }); !errors.Is(err, lease.ErrLeaseConflict) {
		t.Fatalf("busy lease: %v", err)
	}
	if err := leases.Release(t.Context(), held); err != nil {
		t.Fatal(err)
	}
	_, err = f.service.RevokeLabGrant(t.Context(), f.actor, app.ConsoleLabGrantRevokeRequest{GrantID: grant.GrantID, Reason: "revoke orphaned authority", IdempotencyKey: "revoke-orphan", Deadline: f.now.Add(time.Minute).Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(grant.GrantID + "\x00" + string(op.Actor.EffectiveActor) + "\x00" + op.IdempotencyKey))
	id := app.ConsoleLabApprovalPrefix + hex.EncodeToString(digest[:16])
	// The exact issued record remains, but it is never an externally usable bearer reference.
	store := approval.NewStore(filepath.Join(f.root, "approvals"))
	if issued, err := store.LoadIssuedContext(t.Context(), id); err != nil || issued == nil {
		t.Fatalf("missing orphan approval: %v", err)
	}
	if _, err := f.recovery.LoadOperationApprovalReference(t.Context(), op, id); !errors.Is(err, app.ErrInvalidOperationApprovalReference) {
		t.Fatalf("derived reference escaped: %v", err)
	}
	if len(f.provider.inputs) != 0 {
		t.Fatal("denied authority dispatched")
	}
}
