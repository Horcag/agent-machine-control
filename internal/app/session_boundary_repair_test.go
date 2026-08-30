package app_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/sessions"
)

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
