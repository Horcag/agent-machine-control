package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/sessions"
)

func TestSessionMutationReconcilerBatchesThousandsOfFinalizedRecords(t *testing.T) {
	h := newFinalizationHarness(t)
	journalDir := filepath.Join(h.sd.SessionsDir(), "mutations")
	records, err := sessions.NewMutationJournal(journalDir).ListContext(context.Background())
	if err != nil || len(records) != 1 || records[0].Receipt == nil {
		t.Fatalf("seed reservation = %+v, error %v", records, err)
	}
	events, err := audit.NewStore(h.sd.AuditDir()).Tail(100)
	if err != nil {
		t.Fatal(err)
	}
	var seed audit.Event
	for _, event := range events {
		if event.ReceiptID == string(records[0].ReceiptID) {
			seed = event
		}
	}
	seedFinalizedBatch(t, h, journalDir, records[0], seed)
	before, syncs := atomic.LoadInt32(&h.transport.writeCalls), 0
	audits := audit.NewStore(h.sd.AuditDir(), audit.WithSyncDir(func(string) error { syncs++; return nil }))
	svc := h.service(receipt.NewStore(h.sd.ReceiptsDir()), audits, nil)
	if n, err := svc.ReconcileMutationFinalizations(context.Background(), time.Now()); err != nil || n != 0 {
		t.Fatalf("finalized reconciliation = %d, error %v", n, err)
	}
	if syncs != 1 || atomic.LoadInt32(&h.transport.writeCalls) != before {
		t.Fatalf("audit syncs = %d, guest writes = %d; want one sync and unchanged writes", syncs, h.transport.writeCalls)
	}
}

func seedFinalizedBatch(t *testing.T, h *finalizationHarness, journalDir string, original sessions.MutationReservation, seed audit.Event) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(h.sd.AuditDir(), audit.AuditFileName), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(f)
	for i := range 2000 {
		record := original
		r := *record.Receipt
		r.ReceiptID = domain.ReceiptID(fmt.Sprintf("rcpt-%032x", i+1))
		r.IdempotencyKey = fmt.Sprintf("startup-finalized-%04d", i)
		record.IdempotencyKey, record.ReceiptID, record.Receipt = r.IdempotencyKey, r.ReceiptID, &r
		if i%2 == 0 {
			record.SchemaVersion, record.Receipt, record.FinalizationStartedAt = 1, nil, nil
		}
		sum := sha256.Sum256([]byte(record.IdempotencyKey))
		writeBatchFixture(t, filepath.Join(journalDir, fmt.Sprintf("%x.json", sum)), record)
		writeBatchFixture(t, filepath.Join(h.sd.ReceiptsDir(), string(r.ReceiptID)+".json"), receipt.ConvertToDTO(r))
		event := seed
		event.ReceiptID, event.IdempotencyKey = string(r.ReceiptID), r.IdempotencyKey
		if err := encoder.Encode(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeBatchFixture(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSessionMutationReconcilerRejectsFinalizedAuditCorruption(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, corruption := range []string{"duplicate", "identity", "outcome", "envelope", "malformed"} {
			t.Run(fmt.Sprintf("legacy=%t/%s", legacy, corruption), func(t *testing.T) {
				h := newFinalizationHarness(t)
				if legacy {
					rewriteReservationAsLegacy(t, filepath.Join(h.sd.SessionsDir(), "mutations"), "idem-finalization-open")
				}
				corruptFinalizedBatchAudit(t, h, corruption)
				if n, err := restartedFinalizationService(h).ReconcileMutationFinalizations(context.Background(), time.Now()); n != 0 || !errors.Is(err, audit.ErrTerminalEvidenceInvalid) {
					t.Fatalf("corrupt finalized reconciliation = %d, error %v", n, err)
				}
			})
		}
	}
}

func corruptFinalizedBatchAudit(t *testing.T, h *finalizationHarness, corruption string) {
	t.Helper()
	events, err := audit.NewStore(h.sd.AuditDir()).Tail(100)
	if err != nil {
		t.Fatal(err)
	}
	for i := range events {
		if events[i].EventType != audit.EventTerminalOutcome {
			continue
		}
		switch corruption {
		case "duplicate":
			events = append(events, events[i])
		case "identity":
			events[i].Actor = "agent:conflict"
		case "outcome":
			events[i].ExitCode = 9
		case "envelope":
			events[i].SchemaVersion = "0"
		}
		break
	}
	path := filepath.Join(h.sd.AuditDir(), audit.AuditFileName)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(f)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if corruption == "malformed" {
		if err := os.WriteFile(path, []byte("{corrupt\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
