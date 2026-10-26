package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestGuestLabAdmissionSurvivesBackwardClockStep(t *testing.T) {
	h := newGuestHarness(t)
	h.safety.destructive = true
	h.actor.AuthenticatedCaller, h.actor.EffectiveActor = "agent:mcp-local", "agent:mcp-local"
	coordinator := h.service.session
	authority, operator := persistentGuestAuthority(t, h)
	admittedAt := time.Now().UTC()
	authority.recovery.nowFn = func() time.Time { return admittedAt }
	grant, _, err := authority.IssueLabGrant(t.Context(), operator, ConsoleLabGrantIssueRequest{Mode: ConsoleLabModePersistentVM, Target: "default", Reason: "operate disposable VM", IdempotencyKey: "clock-lab", Beneficiary: "agent:mcp-local", AcknowledgeExternalEffects: true})
	if err != nil {
		t.Fatal(err)
	}
	coordinator.labAuthority = authority
	p := h.params("prepare", "clock-step-prepare")
	p.Deadline = admittedAt.Add(time.Minute)
	digest := sha256.Sum256([]byte(grant.GrantID + "\x00" + string(h.actor.EffectiveActor) + "\x00" + p.IdempotencyKey))
	id := ConsoleLabApprovalPrefix + hex.EncodeToString(digest[:16])
	// A server clock can step backwards between issuance and policy/consumption.
	// The separate recovery clock must not choose this admission's issuance time.
	authority.recovery.nowFn = func() time.Time { return admittedAt.Add(50 * time.Millisecond) }
	coordinator.nowFn = func() time.Time {
		if issued, _ := coordinator.approvalStore.LoadIssuedContext(context.Background(), id); issued != nil {
			return admittedAt.Add(-50 * time.Millisecond)
		}
		return admittedAt
	}
	result, err := h.service.Execute(t.Context(), p, nil, nil)
	if err != nil || result.Receipt == nil || result.Receipt.Outcome.Status != domain.OutcomeSuccess || h.transport.executed != 1 {
		t.Fatalf("backwards clock denied fresh local authority: result=%+v err=%v dispatches=%d", result, err, h.transport.executed)
	}
	issued, err := coordinator.approvalStore.LoadIssuedContext(t.Context(), id)
	if err != nil || !issued.IssuedAt.Equal(admittedAt) {
		t.Fatalf("issuance used a different clock: approval=%+v err=%v", issued, err)
	}
	loaded, err := approval.LoadFromFile(filepath.Join(filepath.Dir(h.service.store.directory), "approvals", id+".json"))
	if err != nil || !loaded.Consumed || loaded.ConsumedAt.Before(loaded.IssuedAt) {
		t.Fatalf("invalid durable consumption: approval=%+v err=%v", loaded, err)
	}
	if _, err := h.service.Execute(t.Context(), p, nil, nil); err != nil || h.transport.executed != 1 {
		t.Fatalf("exact retry dispatched again: %v dispatches=%d", err, h.transport.executed)
	}
}

func TestGuestLabClockBoundariesRemainStrict(t *testing.T) {
	for _, scenario := range []string{"existing future", "forward expiry", "consumption expiry", "cancelled"} {
		t.Run(scenario, func(t *testing.T) { testGuestLabClockBoundary(t, scenario) })
	}
}

func testGuestLabClockBoundary(t *testing.T, scenario string) {
	t.Helper()
	h := newGuestHarness(t)
	h.safety.destructive = true
	h.actor.AuthenticatedCaller, h.actor.EffectiveActor = "agent:mcp-local", "agent:mcp-local"
	authority, operator := persistentGuestAuthority(t, h)
	admittedAt := time.Now().UTC()
	authority.recovery.nowFn = func() time.Time { return admittedAt }
	grant, _, err := authority.IssueLabGrant(t.Context(), operator, ConsoleLabGrantIssueRequest{Mode: ConsoleLabModePersistentVM, Target: "default", Reason: "operate disposable VM", IdempotencyKey: "clock-boundaries", Beneficiary: "agent:mcp-local", AcknowledgeExternalEffects: true})
	if err != nil {
		t.Fatal(err)
	}
	coordinator := h.service.session
	coordinator.labAuthority = authority
	p := h.params("prepare", "clock-boundaries-prepare")
	p.Deadline = admittedAt.Add(time.Minute)
	digest := sha256.Sum256([]byte(grant.GrantID + "\x00" + string(h.actor.EffectiveActor) + "\x00" + p.IdempotencyKey))
	id := ConsoleLabApprovalPrefix + hex.EncodeToString(digest[:16])
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if scenario == "existing future" {
		config, err := h.config.Current(ctx, grant.Target, h.actor)
		if err != nil {
			t.Fatal(err)
		}
		op, err := guestOperation(p, grant.Target, config)
		if err != nil {
			t.Fatal(err)
		}
		op.Classification = domain.ClassDestructivePrivileged
		if _, err := authority.deriveLabApproval(ctx, grant, op, admittedAt.Add(50*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	coordinator.nowFn = guestBoundaryClock(coordinator, id, scenario, admittedAt, p.Deadline, cancel)
	_, err = h.service.Execute(ctx, p, nil, nil)
	if err == nil || h.transport.executed != 0 {
		t.Fatalf("clock boundary dispatched: err=%v dispatches=%d", err, h.transport.executed)
	}
	if scenario == "existing future" && !errors.Is(err, domain.ErrApprovalNotYetValid) {
		var denied *PolicyDeniedError
		if !errors.As(err, &denied) || denied.Reason != "approval_record_not_yet_valid" {
			t.Fatalf("future authority changed: %v", err)
		}
	}
	consumed, err := coordinator.approvalStore.IsConsumedContext(t.Context(), id)
	if err != nil || consumed {
		t.Fatalf("rejected authority was consumed: %v %v", consumed, err)
	}
}

func guestBoundaryClock(coordinator *SessionService, id, scenario string, admittedAt, deadline time.Time, cancel context.CancelFunc) func() time.Time {
	afterIssuance := 0
	return func() time.Time {
		if issued, _ := coordinator.approvalStore.LoadIssuedContext(context.Background(), id); issued != nil {
			afterIssuance++
			switch scenario {
			case "forward expiry":
				return deadline
			case "consumption expiry":
				if afterIssuance > 1 {
					return deadline
				}
			case "cancelled":
				cancel()
			}
		}
		return admittedAt
	}
}
