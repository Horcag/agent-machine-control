package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

type clockLabBackend struct {
	Backend
	onCapabilities func()
}

func (b clockLabBackend) Capabilities(context.Context, string) (domain.CapabilitySet, error) {
	b.onCapabilities()
	return domain.NewCapabilitySet(domain.CapabilityConsoleInput), nil
}

func TestPersistentRecoveryLabClockBoundaries(t *testing.T) {
	for _, forward := range []bool{false, true} {
		name := "backward step"
		if forward {
			name = "expiry before consumption"
		}
		t.Run(name, func(t *testing.T) { testPersistentRecoveryLabClock(t, forward) })
	}
}

func testPersistentRecoveryLabClock(t *testing.T, forward bool) {
	t.Helper()
	authority, grant, op, _ := coordinationGrantFixture(t)
	recovery := authority.recovery
	WithRecoveryLabAuthority(authority)(recovery)
	digest := sha256.Sum256([]byte(grant.GrantID + "\x00" + string(op.Actor.EffectiveActor) + "\x00" + op.IdempotencyKey))
	id := ConsoleLabApprovalPrefix + hex.EncodeToString(digest[:16])
	admittedAt := grant.IssuedAt.Add(time.Second)
	op.Deadline = admittedAt.Add(time.Minute)
	authorized := false
	recovery.backend = clockLabBackend{onCapabilities: func() { authorized = true }}
	recovery.nowFn = func() time.Time {
		if forward && authorized {
			return op.Deadline
		}
		if issued, _ := recovery.approvalStore.LoadIssuedContext(context.Background(), id); issued != nil {
			return admittedAt.Add(-50 * time.Millisecond)
		}
		return admittedAt
	}
	req := MutationRequest{TargetID: string(op.Target), Actor: op.Actor, Reason: op.Reason, IdempotencyKey: op.IdempotencyKey, Deadline: op.Deadline}
	locator, err := domain.ParseMachineLocator(string(op.Target))
	if err != nil {
		t.Fatal(err)
	}
	dispatched := 0
	result, err := recovery.executeMutation(t.Context(), op, req, locator.VMID, func(context.Context) error { dispatched++; return nil })
	if forward {
		if err == nil || dispatched != 0 {
			t.Fatalf("expired admission dispatched: result=%+v err=%v dispatches=%d", result, err, dispatched)
		}
	} else if err != nil || result.Outcome.Status != domain.OutcomeSuccess || dispatched != 1 {
		t.Fatalf("backwards clock rejected causal admission: result=%+v err=%v dispatches=%d", result, err, dispatched)
	}
	consumed, err := recovery.approvalStore.IsConsumedContext(t.Context(), id)
	if err != nil || consumed == forward {
		t.Fatalf("consumption=%v err=%v", consumed, err)
	}
}
