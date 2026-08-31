package daemon_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/sessions"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

func TestNewServerReconcilesPendingSessionMutationBeforeServing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	sd, err := statedir.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	scopes := domain.NewScopeSet(domain.ScopeSessionWrite)
	actor, err := domain.NewActorContext("agent:startup-reconcile", "agent:startup-reconcile", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	op := domain.Operation{
		Kind: "session.write", Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Actor: actor,
		Reason: "reconcile interrupted mutation before serving", Deadline: time.Now().Add(time.Minute),
		IdempotencyKey: "idem-startup-reconcile", RequiredCapability: domain.CapabilitySessionWrite,
		RequiredScopes: []string{domain.ScopeSessionWrite}, Classification: domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters: map[string]any{
			"session_id":  "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4",
			"data_sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"data_length": 1,
		},
	}
	journal := sessions.NewMutationJournal(filepath.Join(sd.SessionsDir(), "mutations"))
	if _, err := journal.Reserve(op, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	srv, err := daemon.NewServer(daemon.Config{StateDir: root, Backend: &mockDaemonBackend{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Shutdown(context.Background()) }()

	record, err := journal.Lookup(op)
	if err != nil || record == nil || record.State != sessions.MutationReservationFinalized || record.Receipt == nil {
		t.Fatalf("startup record = %+v err %v", record, err)
	}
	if _, known := record.Result.EffectTruth(record.OperationKind); known {
		t.Fatal("startup reconciliation guessed effect truth for a stale pending reservation")
	}
	rcpt, err := receipt.NewStore(sd.ReceiptsDir()).Get(string(record.ReceiptID))
	if err != nil || rcpt.Outcome.Status != domain.OutcomeFailed {
		t.Fatalf("startup receipt = %+v err %v", rcpt, err)
	}
	if err := audit.NewStore(sd.AuditDir()).VerifyTerminalOutcome(*rcpt); err != nil {
		t.Fatal(err)
	}
}
