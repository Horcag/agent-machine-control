package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/sessions"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

type validationNeverSafety struct{}

func (validationNeverSafety) ResolveSafety(context.Context, domain.MachineRef) (app.SafetyResolution, error) {
	panic("safety resolution must not run for invalid canonical session parameters")
}

func TestSessionOpenCanonicalParametersFailBeforePrivilegedAdmission(t *testing.T) {
	sd, err := statedir.Resolve(t.TempDir())
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

func TestSessionMutationDeadlineIncludesJournalLookupAndCallerBudget(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		context func() (context.Context, context.CancelFunc)
		hook    func(context.Context) sessions.MutationJournalHook
		timeout time.Duration
	}{
		{
			name:    "requested timeout expires during lookup",
			key:     "idem-deadline-requested-lookup",
			context: func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
			hook: func(context.Context) sessions.MutationJournalHook {
				return func(action string) error {
					if action == "lookup" {
						time.Sleep(15 * time.Millisecond)
					}
					return nil
				}
			},
			timeout: 5 * time.Millisecond,
		},
		{
			name: "shorter caller deadline expires during lookup",
			key:  "idem-deadline-caller-lookup",
			context: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 10*time.Millisecond)
			},
			hook: func(ctx context.Context) sessions.MutationJournalHook {
				return func(action string) error {
					if action == "lookup" {
						<-ctx.Done()
					}
					return nil
				}
			},
			timeout: time.Second,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newFinalizationHarness(t)
			ctx, cancel := tt.context()
			defer cancel()
			journal := sessions.NewMutationJournal(filepath.Join(h.sd.SessionsDir(), "mutations"), sessions.WithMutationJournalHook(tt.hook(ctx)))
			svc := h.service(receipt.NewStore(h.sd.ReceiptsDir()), audit.NewStore(h.sd.AuditDir()), journal)
			params := h.writeParams(tt.key)
			params.Timeout = tt.timeout
			before := atomic.LoadInt32(&h.transport.writeCalls)
			_, rcpt, err := svc.WriteSession(ctx, params)
			if !errors.Is(err, context.DeadlineExceeded) || rcpt == nil || rcpt.Outcome.Status != domain.OutcomeAborted {
				t.Fatalf("deadline result = receipt %+v err %v", rcpt, err)
			}
			if atomic.LoadInt32(&h.transport.writeCalls) != before {
				t.Fatal("expired deadline reached transport effect")
			}
		})
	}
}
