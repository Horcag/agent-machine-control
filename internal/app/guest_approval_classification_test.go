package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

type guestApprovalConfigLoader struct {
	config MachineSafetyConfig
	calls  int
	cancel context.CancelFunc
}

func (l *guestApprovalConfigLoader) GetMachineSafetyConfig(domain.MachineRef) (*MachineSafetyConfig, error) {
	l.calls++
	if l.cancel != nil {
		l.cancel()
	}
	value := l.config
	return &value, nil
}

type guestApprovalBackend struct {
	Backend
	calls       int
	checkpoints []domain.CheckpointObservation
}

func (b *guestApprovalBackend) ListCheckpoints(context.Context, string) ([]domain.CheckpointObservation, error) {
	b.calls++
	return append([]domain.CheckpointObservation(nil), b.checkpoints...), nil
}

func guestApprovalFixture(t *testing.T) (*guestHarness, *guestApprovalConfigLoader, *guestApprovalBackend) {
	t.Helper()
	h := newGuestHarness(t)
	h.actor.CallerPermissions[domain.ScopeSessionAdmin] = struct{}{}
	h.actor.EffectivePermissions[domain.ScopeSessionAdmin] = struct{}{}
	loader := &guestApprovalConfigLoader{config: MachineSafetyConfig{RollbackCheckpointID: "e4a523d4-6b99-4d62-a5e2-4752c0f20001"}}
	target := h.service.resolver.(guestTestResolver).resolution.ProviderVMID
	now := time.Now().UTC()
	backend := &guestApprovalBackend{checkpoints: []domain.CheckpointObservation{{ID: loader.config.RollbackCheckpointID, VMID: target, Name: "synthetic rollback", CheckpointType: "Production", CreatedAt: now.Add(-time.Hour), ObservedAt: now, ObservationType: domain.ObservationObserved}}}
	h.service.session.safetyResolver = NewDefaultSafetyResolver(loader, backend)
	return h, loader, backend
}

func TestGuestApprovalClassificationSkipsUncontainedCheckpointsButExecutionRefreshes(t *testing.T) {
	h, loader, backend := guestApprovalFixture(t)
	p := h.params("prepare", "classification-only")
	grant, rcpt, err := h.service.IssueApprovalFor(t.Context(), p, time.Minute, "self")
	if err != nil || grant == nil || rcpt == nil {
		t.Fatalf("issue: %v", err)
	}
	if backend.calls != 0 || loader.calls != 1 {
		t.Fatalf("approval config=%d checkpoints=%d", loader.calls, backend.calls)
	}
	assertGuestApprovalBinding(t, h, p, grant, rcpt)
	p.ApprovalID = grant.ApprovalID
	p.Deadline = grant.Deadline
	result, err := h.service.Execute(t.Context(), p, nil, nil)
	if err != nil || result.Receipt == nil || backend.calls != 3 || loader.calls != 4 || h.transport.prepared != 1 || h.transport.executed != 1 {
		t.Fatalf("execution full safety: calls=%d config=%d result=%+v err=%v", backend.calls, loader.calls, result, err)
	}
	consumed, err := h.service.session.approvalStore.IsConsumedContext(t.Context(), grant.ApprovalID)
	if err != nil || !consumed {
		t.Fatalf("one-use approval not consumed: %v", err)
	}
}

func TestGuestApprovalClassificationIdempotentIssuance(t *testing.T) {
	h, loader, backend := guestApprovalFixture(t)
	p := h.params("prepare", "classification-replay")
	grant, _, err := h.service.IssueApprovalFor(t.Context(), p, time.Minute, "self")
	if err != nil {
		t.Fatal(err)
	}

	replay, _, err := h.service.IssueApprovalFor(t.Context(), p, time.Minute, "self")
	if err != nil || replay.ApprovalID != grant.ApprovalID || !replay.Deadline.Equal(grant.Deadline) {
		t.Fatalf("issuance replay changed: %+v %v", replay, err)
	}
	if backend.calls != 0 || loader.calls != 2 {
		t.Fatalf("issuance replay checkpoint queries=%d config=%d", backend.calls, loader.calls)
	}
}

func assertGuestApprovalBinding(t *testing.T, h *guestHarness, p GuestOperationParams, grant *SessionApprovalGrant, rcpt *domain.Receipt) {
	t.Helper()
	issued, err := h.service.session.approvalStore.LoadIssuedContext(t.Context(), grant.ApprovalID)
	if err != nil {
		t.Fatal(err)
	}
	if issued.Actor != p.Caller.EffectiveActor || issued.IdempotencyKey != p.IdempotencyKey || issued.AuthorizedClass != domain.ClassDestructivePrivileged || !issued.ExpiresAt.Equal(grant.Deadline) {
		t.Fatalf("issued authority changed: %+v", issued)
	}
	if rcpt.Actor != p.Caller.EffectiveActor || rcpt.Validate() != nil {
		t.Fatalf("issuance receipt changed: %+v", rcpt)
	}
	p.Deadline = grant.Deadline
	op, err := guestOperation(p, issued.Target, GuestConfiguration{Generation: h.config.generation, Caller: p.Caller, WorkspaceIdentity: h.config.root})
	if err != nil {
		t.Fatal(err)
	}
	op.Classification = issued.AuthorizedClass
	fingerprint, err := op.Fingerprint()
	if err != nil || issued.Fingerprint != fingerprint {
		t.Fatalf("issued fingerprint changed: %s %v", issued.Fingerprint, err)
	}
}

func TestGuestApprovalContainedInvalidParentRemainsDestructive(t *testing.T) {
	h, loader, backend := guestApprovalFixture(t)
	loader.config.ExternalEffectsContained = true
	backend.checkpoints[0].ParentID = "missing-parent"
	grant, _, err := h.service.IssueApprovalFor(t.Context(), h.params("prepare", "broken-parent"), time.Minute, "self")
	if err != nil || grant == nil || backend.calls != 1 {
		t.Fatalf("invalid chain: %+v %v calls=%d", grant, err, backend.calls)
	}
	issued, err := h.service.session.approvalStore.LoadIssuedContext(t.Context(), grant.ApprovalID)
	if err != nil || issued.AuthorizedClass != domain.ClassDestructivePrivileged {
		t.Fatalf("invalid chain downgraded: %+v %v", issued, err)
	}
}

func TestGuestApprovalContainmentChangeBlocksFinalEffect(t *testing.T) {
	h, loader, backend := guestApprovalFixture(t)
	p := h.params("prepare", "containment-change")
	grant, _, err := h.service.IssueApprovalFor(t.Context(), p, time.Minute, "self")
	if err != nil {
		t.Fatal(err)
	}
	h.transport.prepareHook = func() { loader.config.ExternalEffectsContained = true }
	p.ApprovalID = grant.ApprovalID
	p.Deadline = grant.Deadline
	_, err = h.service.Execute(t.Context(), p, nil, nil)
	if !errors.Is(err, ErrGuestAuthority) || backend.calls != 3 || h.transport.executed != 0 {
		t.Fatalf("changed final safety admitted: %v calls=%d effects=%d", err, backend.calls, h.transport.executed)
	}
	retry, _, err := h.service.IssueApprovalFor(t.Context(), p, time.Minute, "self")
	if err == nil || retry != nil {
		t.Fatal("changed classification reissued original approval")
	}
}

func TestGuestApprovalCanceledConfigReadDoesNotIssue(t *testing.T) {
	h, loader, backend := guestApprovalFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	loader.cancel = cancel
	p := h.params("prepare", "canceled-classification")
	grant, _, err := h.service.IssueApprovalFor(ctx, p, time.Minute, "self")
	if !errors.Is(err, context.Canceled) || grant != nil || backend.calls != 0 {
		t.Fatalf("canceled classification: %+v %v calls=%d", grant, err, backend.calls)
	}
	issued, err := h.service.session.loadIssuedSessionApproval(t.Context(), p.IdempotencyKey)
	if err != nil || issued != nil {
		t.Fatalf("canceled grant persisted: %+v %v", issued, err)
	}
}

func TestGuestApprovalCustomSafetyFallbackRemainsFull(t *testing.T) {
	h, _, _ := guestApprovalFixture(t)
	h.safety.destructive = true
	h.service.session.safetyResolver = h.safety
	grant, _, err := h.service.IssueApprovalFor(t.Context(), h.params("prepare", "custom-classification"), time.Minute, "self")
	if err != nil || grant == nil || h.safety.calls != 1 {
		t.Fatalf("custom safety fallback: %+v %v calls=%d", grant, err, h.safety.calls)
	}
}

type guestApprovalContextLoader struct {
	*guestApprovalConfigLoader
	contextCalls int
}

func (l *guestApprovalContextLoader) GetMachineSafetyConfigContext(ctx context.Context, target domain.MachineRef) (*MachineSafetyConfig, error) {
	l.contextCalls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return l.GetMachineSafetyConfig(target)
}

func TestGuestApprovalContextConfigAndContainedChain(t *testing.T) {
	h, loader, backend := guestApprovalFixture(t)
	contextual := &guestApprovalContextLoader{guestApprovalConfigLoader: loader}
	h.service.session.safetyResolver = NewDefaultSafetyResolver(contextual, backend)
	loader.config.ExternalEffectsContained = true
	grant, _, err := h.service.IssueApprovalFor(t.Context(), h.params("prepare", "contained-valid"), time.Minute, "self")
	if err == nil || grant != nil || backend.calls != 1 || contextual.contextCalls != 1 {
		t.Fatalf("contained verified operation approved: %+v %v queries=%d context=%d", grant, err, backend.calls, contextual.contextCalls)
	}
	loader.config.ExternalEffectsContained = false
	grant, _, err = h.service.IssueApprovalFor(t.Context(), h.params("prepare", "context-config"), time.Minute, "self")
	if err != nil || grant == nil || backend.calls != 1 || contextual.contextCalls != 2 {
		t.Fatalf("contextual classification: %+v %v queries=%d context=%d", grant, err, backend.calls, contextual.contextCalls)
	}
}

type guestApprovalErrorSafety struct{ calls int }

func (r *guestApprovalErrorSafety) ResolveSafety(context.Context, domain.MachineRef) (SafetyResolution, error) {
	r.calls++
	return SafetyResolution{}, errors.New("synthetic safety unavailable")
}

func TestGuestApprovalMissingOrErrorSafetyFallsBackDestructive(t *testing.T) {
	for _, missing := range []bool{true, false} {
		t.Run(map[bool]string{true: "missing", false: "error"}[missing], func(t *testing.T) {
			h, _, backend := guestApprovalFixture(t)
			resolver := &guestApprovalErrorSafety{}
			h.service.session.safetyResolver = resolver
			if missing {
				h.service.session.safetyResolver = nil
			}
			grant, _, err := h.service.IssueApprovalFor(t.Context(), h.params("prepare", "fallback"), time.Minute, "self")
			if err != nil || grant == nil || backend.calls != 0 {
				t.Fatalf("fallback refused: %+v %v", grant, err)
			}
			issued, err := h.service.session.approvalStore.LoadIssuedContext(t.Context(), grant.ApprovalID)
			if err != nil || issued.AuthorizedClass != domain.ClassDestructivePrivileged {
				t.Fatalf("fallback downgraded: %+v %v", issued, err)
			}
			if !missing && resolver.calls != 1 {
				t.Fatal("custom safety not consulted exactly once")
			}
		})
	}
}
