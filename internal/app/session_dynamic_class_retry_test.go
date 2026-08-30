package app_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/sessions"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

type dynamicClassSafetyResolver struct {
	resolution app.SafetyResolution
	calls      atomic.Int32
}

func (r *dynamicClassSafetyResolver) ResolveSafety(context.Context, domain.MachineRef) (app.SafetyResolution, error) {
	r.calls.Add(1)
	return r.resolution, nil
}

type dynamicClassRetryHarness struct {
	svc       *app.SessionService
	transport *trackingTransport
	safety    *dynamicClassSafetyResolver
	actor     domain.ActorContext
	target    string
	now       time.Time
}

func newDynamicClassRetryHarness(t *testing.T, resolution app.SafetyResolution) *dynamicClassRetryHarness {
	t.Helper()

	sd, err := statedir.Resolve(t.TempDir())
	if err != nil {
		t.Fatalf("resolve state directory: %v", err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatalf("ensure state directories: %v", err)
	}

	transport := &trackingTransport{}
	safety := &dynamicClassSafetyResolver{resolution: resolution}
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	mgr := sessions.NewManager(sd.SessionsDir(), transport, time.Now)
	svc := app.NewSessionService(
		mgr,
		safety,
		nil,
		audit.NewStore(sd.AuditDir()),
		receipt.NewStore(sd.ReceiptsDir()),
		approval.NewStore(sd.ApprovalsDir()),
		app.WithSessionClock(func() time.Time { return now }),
	)

	scopes := domain.NewScopeSet(
		domain.ScopeSessionOpen,
		domain.ScopeSessionRead,
		domain.ScopeSessionWrite,
		domain.ScopeSessionClose,
		domain.ScopeSessionAdmin,
	)
	actor, err := domain.NewActorContext("agent:dynamic-retry", "agent:dynamic-retry", scopes, scopes)
	if err != nil {
		t.Fatalf("create actor: %v", err)
	}

	return &dynamicClassRetryHarness{
		svc:       svc,
		transport: transport,
		safety:    safety,
		actor:     actor,
		target:    "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		now:       now,
	}
}

func destructiveSafetyResolution() app.SafetyResolution {
	return app.SafetyResolution{Classification: domain.ClassDestructivePrivileged}
}

func reversibleSafetyResolution() app.SafetyResolution {
	return app.SafetyResolution{
		Classification: domain.ClassReversibleMutation,
		Contained:      true,
		RollbackRef:    "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
		RollbackState: policy.RollbackState{
			Available:    true,
			Verified:     true,
			CheckpointID: "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
		},
	}
}

func (h *dynamicClassRetryHarness) openParams(key string) app.SessionOpenParams {
	return app.SessionOpenParams{
		Target:         h.target,
		Caller:         h.actor,
		Reason:         "open dynamically classified session",
		IdempotencyKey: key,
		Timeout:        30 * time.Second,
		Cols:           80,
		Rows:           24,
	}
}

func destructiveOpenApproval(t *testing.T, h *dynamicClassRetryHarness, params app.SessionOpenParams) *domain.Approval {
	t.Helper()

	op := domain.Operation{
		Kind:                "session.open",
		Target:              domain.MachineRef(params.Target),
		Actor:               params.Caller,
		Reason:              params.Reason,
		Deadline:            h.now.Add(params.Timeout),
		IdempotencyKey:      params.IdempotencyKey,
		RequiredCapability:  domain.CapabilitySessionOpen,
		RequiredScopes:      []string{domain.ScopeSessionOpen},
		Classification:      domain.ClassDestructivePrivileged,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters: map[string]any{
			"cols": uint16(80),
			"rows": uint16(24),
			"term": domain.DefaultTermType,
		},
	}
	fingerprint, err := op.Fingerprint()
	if err != nil {
		t.Fatalf("compute approval fingerprint: %v", err)
	}

	return &domain.Approval{
		ID:              "app-0123456789abcdef0123456789abcdef",
		Actor:           params.Caller.EffectiveActor,
		Target:          domain.MachineRef(params.Target),
		AuthorizedClass: domain.ClassDestructivePrivileged,
		Fingerprint:     fingerprint,
		IdempotencyKey:  params.IdempotencyKey,
		IssuedAt:        h.now.Add(-time.Minute),
		ExpiresAt:       h.now.Add(time.Hour),
	}
}

func assertTransportEffects(t *testing.T, transport *trackingTransport, dial int32) {
	t.Helper()
	if got := atomic.LoadInt32(&transport.dialCalls); got != dial {
		t.Errorf("dial calls = %d, want %d", got, dial)
	}
	if got := atomic.LoadInt32(&transport.writeCalls); got != 0 {
		t.Errorf("write calls = %d, want 0", got)
	}
	if got := atomic.LoadInt32(&transport.controlCalls); got != 0 {
		t.Errorf("control calls = %d, want 0", got)
	}
	if got := atomic.LoadInt32(&transport.closeCalls); got != 0 {
		t.Errorf("close calls = %d, want 0", got)
	}
}

func requirePolicyDenial(t *testing.T, err error) *app.PolicyDeniedError {
	t.Helper()
	if err == nil {
		t.Fatal("expected policy denial")
	}
	var denied *app.PolicyDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("expected PolicyDeniedError, got %v", err)
	}
	return denied
}

func TestSessionDynamicClassDeniedExactRetryReturnsCachedReceipt(t *testing.T) {
	h := newDynamicClassRetryHarness(t, destructiveSafetyResolution())
	params := h.openParams("idem-dynamic-denied-retry")

	obs1, receipt1, err1 := h.svc.OpenSession(context.Background(), params)
	denial1 := requirePolicyDenial(t, err1)
	if obs1 != nil || receipt1 == nil || receipt1.Outcome.Status != domain.OutcomeDenied {
		t.Fatalf("first denial = obs %v, receipt %+v", obs1, receipt1)
	}
	assertTransportEffects(t, h.transport, 0)

	obs2, receipt2, err2 := h.svc.OpenSession(context.Background(), params)
	denial2 := requirePolicyDenial(t, err2)
	if obs2 != nil || receipt2 == nil || receipt2.ReceiptID != receipt1.ReceiptID {
		t.Fatalf("retry denial = obs %v, receipt %+v", obs2, receipt2)
	}
	if denial2.Reason != denial1.Reason || denial2.Message != denial1.Message {
		t.Errorf("retry denial changed: first=%+v retry=%+v", denial1, denial2)
	}
	if got := h.safety.calls.Load(); got != 1 {
		t.Errorf("safety resolutions = %d, want 1", got)
	}
	assertTransportEffects(t, h.transport, 0)
}

func TestSessionDynamicClassApprovedExactRetryPrecedesConsumedApproval(t *testing.T) {
	h := newDynamicClassRetryHarness(t, destructiveSafetyResolution())
	params := h.openParams("idem-dynamic-approved-retry")
	params.Approval = destructiveOpenApproval(t, h, params)

	obs1, receipt1, err := h.svc.OpenSession(context.Background(), params)
	if err != nil || obs1 == nil || receipt1 == nil || receipt1.Outcome.Status != domain.OutcomeSuccess {
		t.Fatalf("approved open failed: obs=%v receipt=%+v err=%v", obs1, receipt1, err)
	}
	assertTransportEffects(t, h.transport, 1)

	obs2, receipt2, err := h.svc.OpenSession(context.Background(), params)
	if err != nil || obs2 == nil || receipt2 == nil {
		t.Fatalf("approved retry failed: obs=%v receipt=%+v err=%v", obs2, receipt2, err)
	}
	if receipt2.ReceiptID != receipt1.ReceiptID || obs2.ID != obs1.ID {
		t.Errorf("approved retry changed durable result: first=%+v retry=%+v", receipt1, receipt2)
	}
	if got := h.safety.calls.Load(); got != 1 {
		t.Errorf("safety resolutions = %d, want 1", got)
	}
	assertTransportEffects(t, h.transport, 1)
}

func TestSessionDynamicClassRetryIgnoresCurrentSafetyStateAfterTerminalReceipt(t *testing.T) {
	h := newDynamicClassRetryHarness(t, destructiveSafetyResolution())
	params := h.openParams("idem-dynamic-safety-drift")

	_, receipt1, err := h.svc.OpenSession(context.Background(), params)
	_ = requirePolicyDenial(t, err)
	if receipt1 == nil {
		t.Fatal("first denial did not persist a receipt")
	}

	h.safety.resolution = reversibleSafetyResolution()
	_, receipt2, err := h.svc.OpenSession(context.Background(), params)
	_ = requirePolicyDenial(t, err)
	if receipt2 == nil || receipt2.ReceiptID != receipt1.ReceiptID {
		t.Fatalf("retry after safety drift did not return cached receipt: %+v", receipt2)
	}
	if got := h.safety.calls.Load(); got != 1 {
		t.Errorf("retry re-resolved drifted safety state: calls=%d", got)
	}
	assertTransportEffects(t, h.transport, 0)
}

func TestSessionDynamicClassCollisionsRemainFailClosed(t *testing.T) {
	h := newDynamicClassRetryHarness(t, destructiveSafetyResolution())
	params := h.openParams("idem-dynamic-collision")
	params.Approval = destructiveOpenApproval(t, h, params)

	obs, firstReceipt, err := h.svc.OpenSession(context.Background(), params)
	if err != nil || obs == nil || firstReceipt == nil {
		t.Fatalf("approved open failed: obs=%v receipt=%+v err=%v", obs, firstReceipt, err)
	}

	otherActor, err := domain.NewActorContext(
		"agent:other",
		"agent:other",
		domain.NewScopeSet(domain.ScopeSessionOpen),
		domain.NewScopeSet(domain.ScopeSessionOpen),
	)
	if err != nil {
		t.Fatalf("create other actor: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*app.SessionOpenParams)
	}{
		{name: "parameters", mutate: func(p *app.SessionOpenParams) { p.Cols = 120 }},
		{name: "target", mutate: func(p *app.SessionOpenParams) { p.Target = "c4a523d4-6b99-4d62-a5e2-4752c0f20002" }},
		{name: "actor", mutate: func(p *app.SessionOpenParams) { p.Caller = otherActor }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := params
			tt.mutate(&changed)
			changedObs, changedReceipt, err := h.svc.OpenSession(context.Background(), changed)
			if !errors.Is(err, receipt.ErrIdempotencyCollision) {
				t.Fatalf("error = %v, want ErrIdempotencyCollision", err)
			}
			if changedObs != nil || changedReceipt != nil {
				t.Fatalf("collision disclosed cached data: obs=%v receipt=%+v", changedObs, changedReceipt)
			}
		})
	}

	_, kindReceipt, err := h.svc.WriteSession(context.Background(), app.SessionWriteParams{
		SessionID:      obs.ID,
		Caller:         h.actor,
		Data:           "hostname\r\n",
		Reason:         params.Reason,
		IdempotencyKey: params.IdempotencyKey,
	})
	if !errors.Is(err, receipt.ErrIdempotencyCollision) {
		t.Fatalf("kind collision error = %v, want ErrIdempotencyCollision", err)
	}
	if kindReceipt != nil {
		t.Fatalf("kind collision disclosed cached receipt: %+v", kindReceipt)
	}

	if got := h.safety.calls.Load(); got != 1 {
		t.Errorf("collisions reached safety resolution: calls=%d", got)
	}
	assertTransportEffects(t, h.transport, 1)
}
