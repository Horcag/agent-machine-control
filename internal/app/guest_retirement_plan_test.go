package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
)

func retirementPlan(t *testing.T, h *guestHarness) *guestRetirementPlan {
	t.Helper()
	p := h.params("put", "plan")
	p.Size, p.SHA256 = 4, guestSHA("data")
	resolution, config, err := h.service.resolve(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	p.Target, p.Caller = resolution.Locator.String(), config.Caller
	op, err := guestOperation(p, domain.MachineRef(p.Target), config)
	if err != nil {
		t.Fatal(err)
	}
	return h.service.readonlyRetirementPlan(t.Context(), p, op, config, guestPoolKey(op.Target, op.Actor, config.Generation))
}

func TestGuestReadonlyRetirementUsesFinalFreshAuthority(t *testing.T) {
	h := newGuestHarness(t)
	h.prepare(t)
	if h.safety.calls != 3 {
		t.Fatalf("prepare gates changed: %d", h.safety.calls)
	}
	h.safety.calls, h.config.calls = 0, 0
	h.put(t, "warm")
	if h.safety.calls != 2 || h.config.calls != 2 {
		t.Fatalf("warm authority gates: safety=%d config=%d", h.safety.calls, h.config.calls)
	}
	plan := retirementPlan(t, h)
	if plan == nil || !plan.consume(plan.operation, plan.config, plan.key) || plan.consume(plan.operation, plan.config, plan.key) {
		t.Fatal("plan is not invocation-local one-use proof")
	}
	plan = retirementPlan(t, h)
	changed := plan.config
	changed.Generation = "other"
	if plan.consume(plan.operation, changed, plan.key) {
		t.Fatal("plan accepted different configuration")
	}
}

func TestGuestReadonlyRetirementRejectsIncompleteOriginalInventory(t *testing.T) {
	for _, tc := range []struct {
		name, suffix string
		drift        func(*guestWorkerRecord)
	}{
		{"missing_launch", ".launch", nil}, {"missing_worker", ".worker", nil}, {"missing_ready", ".ready", nil},
		{"birth", ".worker", func(r *guestWorkerRecord) { r.Identity.BirthTicks++ }},
		{"root", ".ready", func(r *guestWorkerRecord) { r.Root.FileID = strings.Repeat("f", 32) }},
		{"actor", ".worker", func(r *guestWorkerRecord) { r.Actor = "foreign" }},
		{"pending", ".retirement", func(_ *guestWorkerRecord) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newGuestHarness(t)
			h.prepare(t)
			_, current := currentReplacementRecord(t, h)
			changePlanDocument(t, h, current, tc.suffix, tc.drift)
			if retirementPlan(t, h) != nil {
				t.Fatal("uncertain inventory enabled shortcut")
			}
		})
	}
	h := newGuestHarness(t)
	h.prepare(t)
	h.service.workers = map[string]guestWorkerRecord{}
	if retirementPlan(t, h) != nil {
		t.Fatal("missing current enabled shortcut")
	}
}

func TestGuestReadonlyRetirementRequiresExactRetiredRecords(t *testing.T) {
	h, _, _, _ := replacementHarness(t)
	_, old := currentReplacementRecord(t, h)
	if _, err := h.service.Execute(t.Context(), h.params("prepare", "replacement"), nil, nil); err != nil {
		t.Fatal(err)
	}
	if retirementPlan(t, h) == nil {
		t.Fatal("complete retired original refused")
	}
	path := filepath.Join(h.service.store.directory, old.RecordID+".retired")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	old.Identity.BirthTicks++
	if err := h.service.store.write(t.Context(), old.RecordID, ".retired", old); err != nil {
		t.Fatal(err)
	}
	if retirementPlan(t, h) != nil {
		t.Fatal("partial retired record accepted")
	}
}

func TestGuestReadonlyRetirementFinalDriftNeverDispatches(t *testing.T) {
	for _, field := range []string{"rollback", "generation", "root", "permissions"} {
		t.Run(field, func(t *testing.T) {
			h := newGuestHarness(t)
			h.prepare(t)
			h.safety.calls, h.config.calls = 0, 0
			if field == "rollback" {
				h.safety.classes = []domain.OperationClass{domain.ClassReversibleMutation, domain.ClassDestructivePrivileged}
			} else {
				h.config.change = func(c *GuestConfiguration, call int) {
					if call != 2 {
						return
					}
					switch field {
					case "generation":
						c.Generation = "changed"
					case "root":
						c.WorkspaceIdentity.FileID = strings.Repeat("f", 32)
					case "permissions":
						delete(c.Caller.EffectivePermissions, domain.ScopeSessionOpen)
					}
				}
			}
			p := h.params("put", "drift")
			p.Size, p.SHA256 = 4, guestSHA("data")
			_, err := h.service.Execute(t.Context(), p, stream.BytesInput([]byte("data")), nil)
			if !errors.Is(err, ErrGuestAuthority) || h.transport.executed != 1 {
				t.Fatalf("final drift crossed business boundary: %v executed=%d", err, h.transport.executed)
			}
		})
	}
}

func TestGuestReadonlyRetirementDoesNotReenterEffectfulScan(t *testing.T) {
	h, _, config, events := replacementHarness(t)
	_, old := currentReplacementRecord(t, h)
	if _, err := h.service.Execute(t.Context(), h.params("prepare", "replacement"), nil, nil); err != nil {
		t.Fatal(err)
	}
	plan := retirementPlan(t, h)
	if plan == nil {
		t.Fatal("complete inventory did not produce a plan")
	}
	path := filepath.Join(h.service.store.directory, old.RecordID+".retired")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	config.absent = true
	*events = nil
	executed, retired := h.transport.executed, h.transport.retired
	p := h.params("put", "plan")
	p.Size, p.SHA256 = 4, guestSHA("data")
	p.Target, p.Caller = string(plan.operation.Target), plan.config.Caller
	effect := false
	// Without coordinator admission, the final authority gate must refuse. A
	// re-entered retirement scan would confirm and recreate the removed record
	// before that gate, exposing the unsafe proof/consumption ordering.
	_, err := h.service.run(t.Context(), p, plan.operation, plan.config, plan.key, strings.Repeat("a", 64), stream.BytesInput([]byte("data")), nil, &effect, plan)
	if !errors.Is(err, ErrGuestAuthority) || effect || !plan.consumed || len(*events) != 0 || h.transport.executed != executed || h.transport.retired != retired {
		t.Fatalf("proof re-entered retirement: %v events=%v", err, *events)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only plan recreated retirement evidence: %v", err)
	}
}

func changePlanDocument(t *testing.T, h *guestHarness, current guestWorkerRecord, suffix string, drift func(*guestWorkerRecord)) {
	t.Helper()
	path := filepath.Join(h.service.store.directory, current.RecordID+suffix)
	if suffix != ".retirement" {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	if drift == nil {
		return
	}
	drift(&current)
	if err := h.service.store.write(t.Context(), current.RecordID, suffix, current); err != nil {
		t.Fatal(err)
	}
}

func TestGuestReadonlyRetirementLimitAndCancellation(t *testing.T) {
	h := newGuestHarness(t)
	h.prepare(t)
	plan := retirementPlan(t, h)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if h.service.readonlyRetirementPlan(ctx, GuestOperationParams{Operation: "put"}, plan.operation, plan.config, plan.key) != nil {
		t.Fatal("cancelled proof accepted")
	}
	_, current := currentReplacementRecord(t, h)
	for i := range 128 {
		previous := current
		previous.RecordID = fmt.Sprintf("%064x", i+1)
		previous.Identity.PID += i + 1
		previous.Identity.BirthTicks += int64(i + 1)
		for _, suffix := range []string{".launch", ".worker", ".ready", ".retired"} {
			if err := h.service.store.write(t.Context(), previous.RecordID, suffix, previous); err != nil {
				t.Fatal(err)
			}
		}
	}
	if retirementPlan(t, h) != nil {
		t.Fatal("129 matching original launches accepted")
	}
}

func TestGuestReadonlyRetirementPendingUsesOldAuthorityBarrier(t *testing.T) {
	h, _, _, events := replacementHarness(t)
	_, current := currentReplacementRecord(t, h)
	if err := h.service.store.write(t.Context(), current.RecordID, ".retirement", current); err != nil {
		t.Fatal(err)
	}
	h.config.calls, h.safety.calls = 0, 0
	h.config.change = func(c *GuestConfiguration, call int) {
		if call >= 2 {
			c.Generation = "changed"
		}
	}
	p := h.params("put", "pending")
	p.Size, p.SHA256 = 4, guestSHA("data")
	_, err := h.service.Execute(t.Context(), p, stream.BytesInput([]byte("data")), nil)
	if !errors.Is(err, ErrGuestAuthority) || len(*events) != 0 || h.safety.calls != 1 {
		t.Fatalf("pending crossed early authority: %v events=%v safety=%d", err, *events, h.safety.calls)
	}
}
