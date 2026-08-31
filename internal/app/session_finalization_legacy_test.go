package app_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

func TestSessionMutationReconcilerVerifiesLegacyFinalizedEvidence(t *testing.T) {
	h := newFinalizationHarness(t)
	svc := h.service(receipt.NewStore(h.sd.ReceiptsDir()), audit.NewStore(h.sd.AuditDir()), nil)
	params := h.writeParams("idem-legacy-finalized")
	n, original, err := svc.WriteSession(context.Background(), params)
	if err != nil || n != len(params.Data) || original == nil {
		t.Fatalf("initial write = n %d receipt %+v err %v", n, original, err)
	}
	rewriteReservationAsLegacy(t, filepath.Join(h.sd.SessionsDir(), "mutations"), params.IdempotencyKey)

	before := atomic.LoadInt32(&h.transport.writeCalls)
	restarted := restartedFinalizationService(h)
	if reconciled, err := restarted.ReconcileMutationFinalizations(context.Background(), time.Now()); err != nil || reconciled != 0 {
		t.Fatalf("legacy reconciliation = %d err %v", reconciled, err)
	}
	n, replayed, err := restarted.WriteSession(context.Background(), params)
	if err != nil || n != len(params.Data) || replayed == nil || replayed.ReceiptID != original.ReceiptID {
		t.Fatalf("legacy replay = n %d receipt %+v err %v", n, replayed, err)
	}
	if atomic.LoadInt32(&h.transport.writeCalls) != before {
		t.Fatal("legacy finalized replay duplicated the transport effect")
	}
}

func rewriteReservationAsLegacy(t *testing.T, dir, idempotencyKey string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var record map[string]any
		if err := json.Unmarshal(data, &record); err != nil {
			t.Fatal(err)
		}
		if record["idempotency_key"] != idempotencyKey {
			continue
		}
		record["schema_version"] = float64(1)
		delete(record, "receipt")
		delete(record, "finalization_started_at")
		legacy, err := json.MarshalIndent(record, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, legacy, 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatalf("mutation reservation %q not found", idempotencyKey)
}
