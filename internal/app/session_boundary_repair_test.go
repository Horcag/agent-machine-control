package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/policy"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/sessions"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

type validationNeverSafety struct{}

func (validationNeverSafety) ResolveSafety(context.Context, domain.MachineRef) (app.SafetyResolution, error) {
	panic("safety resolution must not run for invalid canonical session parameters")
}

func TestSessionOpenCanonicalParametersFailBeforePrivilegedAdmission(t *testing.T) {
	sd, err := statedir.Resolve(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	var journalCalls, receiptCalls, auditCalls atomic.Int32
	journal := sessions.NewMutationJournal(filepath.Join(sd.SessionsDir(), "mutations"), sessions.WithMutationJournalHook(func(string) error {
		journalCalls.Add(1)
		return nil
	}))
	receipts := receipt.NewStore(sd.ReceiptsDir(), receipt.WithSaveHook(func(domain.Receipt) error {
		receiptCalls.Add(1)
		return nil
	}))
	audits := audit.NewStore(sd.AuditDir(), audit.WithAppendHook(func(audit.Event) error {
		auditCalls.Add(1)
		return nil
	}))
	transport := &trackingTransport{panicOnDial: true}
	mgr := sessions.NewManager(sd.SessionsDir(), transport, time.Now)
	leases := lease.NewManager(sd.LeasesDir())
	svc := app.NewSessionService(
		mgr, validationNeverSafety{}, leases, audits, receipts, approval.NewStore(sd.ApprovalsDir()),
		app.WithSessionMutationJournal(journal),
	)
	scopes := domain.NewScopeSet(domain.ScopeSessionOpen, domain.ScopeSessionAdmin)
	actor, err := domain.NewActorContext("agent:validation", "agent:validation", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		cols uint16
		rows uint16
		term string
	}{
		{name: "cols", cols: 1, rows: 24, term: domain.DefaultTermType},
		{name: "rows", cols: 80, rows: 1, term: domain.DefaultTermType},
		{name: "term", cols: 80, rows: 24, term: "not a terminal type"},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs, rcpt, err := svc.OpenSession(context.Background(), app.SessionOpenParams{
				Target:         "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
				Caller:         actor,
				Reason:         "reject invalid canonical terminal parameters",
				IdempotencyKey: "invalid-canonical-" + tt.name,
				Cols:           tt.cols,
				Rows:           tt.rows,
				Term:           tt.term,
				Approval:       &domain.Approval{ID: "app-0123456789abcdef0123456789abcdef"},
			})
			if obs != nil || rcpt != nil || !errors.Is(err, domain.ErrNonCanonicalParameter) {
				t.Fatalf("invalid open %d = obs %+v receipt %+v err %v", i, obs, rcpt, err)
			}
		})
	}

	if got := atomic.LoadInt32(&transport.dialCalls); got != 0 {
		t.Fatalf("transport dial calls = %d, want 0", got)
	}
	if journalCalls.Load() != 0 || receiptCalls.Load() != 0 || auditCalls.Load() != 0 {
		t.Fatalf("durable side effects = journal %d receipt %d audit %d, want zero", journalCalls.Load(), receiptCalls.Load(), auditCalls.Load())
	}
	for name, dir := range map[string]string{"approvals": sd.ApprovalsDir(), "leases": sd.LeasesDir(), "sessions": sd.SessionsDir()} {
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(entries) != 0 {
			t.Fatalf("%s side effects = %v, want empty", name, entries)
		}
	}
}

func TestSessionWriteAcceptedBytesRemainFailedTruthAndExactRetryDoesNotReplay(t *testing.T) {
	h := newFinalizationHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	h.transport.cancelOnWrite = cancel
	svc := h.service(receipt.NewStore(h.sd.ReceiptsDir()), audit.NewStore(h.sd.AuditDir()), nil)
	params := h.writeParams("idem-positive-byte-cancel")
	n, rcpt, err := svc.WriteSession(ctx, params)
	if n != len(params.Data) || !errors.Is(err, context.Canceled) || rcpt == nil {
		t.Fatalf("write = n %d receipt %+v err %v", n, rcpt, err)
	}
	if rcpt.Outcome.Status != domain.OutcomeFailed {
		t.Fatalf("outcome = %+v, want failed indeterminate effect", rcpt.Outcome)
	}
	if rcpt.RollbackRef == "" || len(rcpt.EvidenceRefs) != 1 || rcpt.EvidenceRefs[0] != string(h.obs.ID) {
		t.Fatalf("rollback/evidence = %q/%v", rcpt.RollbackRef, rcpt.EvidenceRefs)
	}
	before := atomic.LoadInt32(&h.transport.writeCalls)
	h.transport.cancelOnWrite = nil
	n, retryReceipt, retryErr := svc.WriteSession(context.Background(), params)
	if n != len(params.Data) || retryReceipt == nil || retryReceipt.ReceiptID != rcpt.ReceiptID || retryErr == nil {
		t.Fatalf("retry = n %d receipt %+v err %v", n, retryReceipt, retryErr)
	}
	if atomic.LoadInt32(&h.transport.writeCalls) != before {
		t.Fatal("exact retry duplicated accepted transport bytes")
	}
}

type durableLookupInstaller func(*statedir.StateDir, chan struct{}) (*sessions.MutationJournal, *receipt.Store)

func TestSessionMutationDeadlineStopsInsideDurableRetryLookups(t *testing.T) {
	tests := []struct {
		name    string
		install durableLookupInstaller
	}{
		{
			name: "journal lookup",
			install: func(sd *statedir.StateDir, entered chan struct{}) (*sessions.MutationJournal, *receipt.Store) {
				journal := sessions.NewMutationJournal(filepath.Join(sd.SessionsDir(), "mutations"), sessions.WithMutationJournalLookupHook(func(ctx context.Context) error {
					close(entered)
					<-ctx.Done()
					return ctx.Err()
				}))
				return journal, receipt.NewStore(sd.ReceiptsDir())
			},
		},
		{
			name: "receipt lookup",
			install: func(sd *statedir.StateDir, entered chan struct{}) (*sessions.MutationJournal, *receipt.Store) {
				receipts := receipt.NewStore(sd.ReceiptsDir(), receipt.WithLookupHook(func(ctx context.Context) error {
					close(entered)
					<-ctx.Done()
					return ctx.Err()
				}))
				return sessions.NewMutationJournal(filepath.Join(sd.SessionsDir(), "mutations")), receipts
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runSessionMutationLookupDeadline(t, tt.name, tt.install)
		})
	}
}

func runSessionMutationLookupDeadline(t *testing.T, name string, install durableLookupInstaller) {
	t.Helper()
	sd, err := statedir.Resolve(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	transport := &trackingTransport{}
	manager := sessions.NewManager(sd.SessionsDir(), transport, time.Now)
	scopes := domain.NewScopeSet(domain.ScopeSessionOpen, domain.ScopeSessionWrite)
	actor, err := domain.NewActorContext("agent:lookup-deadline", "agent:lookup-deadline", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := manager.Open(context.Background(), domain.Operation{
		Kind: "session.open", Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Actor: actor, IdempotencyKey: "setup-lookup-deadline",
	}, 80, 24, domain.DefaultTermType)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	journal, receipts := install(sd, entered)
	var auditAppends atomic.Int32
	audits := audit.NewStore(sd.AuditDir(), audit.WithAppendHook(func(audit.Event) error { auditAppends.Add(1); return nil }))
	safety := &mockSafetyResolver{resolution: app.SafetyResolution{
		Classification: domain.ClassReversibleMutation, Contained: true,
		RollbackRef:   "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
		RollbackState: policy.RollbackState{Available: true, Verified: true, CheckpointID: "e4a523d4-6b99-4d62-a5e2-4752c0f20001"},
	}}
	svc := app.NewSessionService(manager, safety, lease.NewManager(sd.LeasesDir()), audits, receipts, approval.NewStore(sd.ApprovalsDir()), app.WithSessionMutationJournal(journal))
	before := atomic.LoadInt32(&transport.writeCalls)
	_, rcpt, err := svc.WriteSession(context.Background(), app.SessionWriteParams{
		SessionID: opened.ID, Caller: actor, Data: "blocked lookup", Reason: "deadline includes durable retry lookup",
		IdempotencyKey: "idem-" + strings.ReplaceAll(name, " ", "-"), Timeout: 500 * time.Millisecond,
	})
	if !errors.Is(err, context.DeadlineExceeded) || rcpt != nil {
		t.Fatalf("deadline result = receipt %+v err %v, want no receipt and deadline", rcpt, err)
	}
	select {
	case <-entered:
	default:
		t.Fatal("durable lookup hook was not entered")
	}
	if atomic.LoadInt32(&transport.writeCalls) != before || auditAppends.Load() != 0 {
		t.Fatalf("post-lookup side effects: writes=%d audit=%d", transport.writeCalls, auditAppends.Load())
	}
	assertLookupDeadlineLeftNoDurableState(t, sd)
}

func assertLookupDeadlineLeftNoDurableState(t *testing.T, sd *statedir.StateDir) {
	t.Helper()
	receiptEntries, err := os.ReadDir(sd.ReceiptsDir())
	if err != nil || len(receiptEntries) != 0 {
		t.Fatalf("receipt state after lookup deadline = %v err %v", receiptEntries, err)
	}
	leaseEntries, err := os.ReadDir(sd.LeasesDir())
	if err != nil || len(leaseEntries) != 0 {
		t.Fatalf("lease state after lookup deadline = %v err %v", leaseEntries, err)
	}
	if _, err := os.Stat(filepath.Join(sd.SessionsDir(), "mutations")); !os.IsNotExist(err) {
		t.Fatalf("mutation journal created during lookup: %v", err)
	}
}
