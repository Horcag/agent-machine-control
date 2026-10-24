package app

import (
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestGuestPrepareReturnsOriginalRecordAfterReceiptReclassification(t *testing.T) {
	h := newGuestHarness(t)
	h.safety.destructive = true
	h.actor.CallerPermissions[domain.ScopeSessionAdmin] = struct{}{}
	h.actor.EffectivePermissions[domain.ScopeSessionAdmin] = struct{}{}
	p := h.params("prepare", "original-record")
	grant, _, err := h.service.IssueApprovalFor(t.Context(), p, time.Minute, "self")
	if err != nil {
		t.Fatal(err)
	}
	p.Deadline, p.ApprovalID = grant.Deadline, grant.ApprovalID
	out, err := h.service.Execute(t.Context(), p, nil, nil)
	if err != nil || out.Receipt == nil || len(out.WorkerRecordID) != 64 || out.WorkerRecordID == strings.TrimPrefix(string(out.Receipt.IdempotencyFingerprint), "sha256:") {
		t.Fatalf("original record was inferred from classified receipt: %+v %v", out, err)
	}
	assertGuestReturnedOriginalRecord(t, h, out)
	cached, err := h.service.Execute(t.Context(), p, nil, nil)
	if err != nil || !cached.CachedReceipt || cached.WorkerRecordID != out.WorkerRecordID || cached.Worker != out.Worker || h.transport.prepared != 1 {
		t.Fatalf("cached original drift: %+v %v", cached, err)
	}
}

func assertGuestReturnedOriginalRecord(t *testing.T, h *guestHarness, out GuestOperationResult) {
	t.Helper()
	var err error
	var original, ready guestWorkerRecord
	if err = h.service.store.read(t.Context(), out.WorkerRecordID, ".worker", &original); err != nil {
		t.Fatal(err)
	}
	if err = h.service.store.read(t.Context(), out.WorkerRecordID, ".ready", &ready); err != nil || ready != original || original.RecordID != out.WorkerRecordID || original.Identity != out.Worker || original.Actor != out.Receipt.Actor {
		t.Fatalf("unproven original: %+v %v", original, err)
	}
}
