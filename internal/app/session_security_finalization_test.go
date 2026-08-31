package app_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
	"github.com/Horcag/agent-machine-control/internal/policy"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/sessions"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

type diagnosticNeverTransport struct{}

func (diagnosticNeverTransport) Dial(context.Context, domain.MachineRef, uint16, uint16, string) (guestssh.Channel, error) {
	return nil, errors.New("transport must not be called for denied operations")
}

type diagnosticDestructiveSafety struct{}

func (diagnosticDestructiveSafety) ResolveSafety(context.Context, domain.MachineRef) (app.SafetyResolution, error) {
	return app.SafetyResolution{Classification: domain.ClassDestructivePrivileged}, nil
}

type diagnosticReversibleSafety struct{}

func (diagnosticReversibleSafety) ResolveSafety(context.Context, domain.MachineRef) (app.SafetyResolution, error) {
	const checkpointID = "e4a523d4-6b99-4d62-a5e2-4752c0f20001"
	return app.SafetyResolution{
		Classification: domain.ClassReversibleMutation,
		Contained:      true,
		RollbackRef:    checkpointID,
		RollbackState: policy.RollbackState{
			Available:    true,
			Verified:     true,
			CheckpointID: checkpointID,
		},
	}, nil
}

type expiringAdmissionSafety struct {
	calls atomic.Int32
}

func (s *expiringAdmissionSafety) ResolveSafety(ctx context.Context, target domain.MachineRef) (app.SafetyResolution, error) {
	if s.calls.Add(1) == 1 {
		<-ctx.Done()
		return app.SafetyResolution{Classification: domain.ClassDestructivePrivileged}, nil
	}
	return diagnosticReversibleSafety{}.ResolveSafety(ctx, target)
}

type diagnosticCloseErrorTransport struct {
	channel *diagnosticCloseErrorChannel
}

func (t diagnosticCloseErrorTransport) Dial(context.Context, domain.MachineRef, uint16, uint16, string) (guestssh.Channel, error) {
	return t.channel, nil
}

type diagnosticCloseErrorChannel struct {
	closed chan struct{}
	once   sync.Once
}

func (c *diagnosticCloseErrorChannel) Read([]byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}

func (c *diagnosticCloseErrorChannel) Write(context.Context, []byte) (int, error) {
	return 0, nil
}

func (c *diagnosticCloseErrorChannel) SendControl(context.Context, domain.ControlKey) (guestssh.ControlResult, error) {
	return guestssh.ControlResult{AcceptedBytes: 1, EffectApplied: true}, nil
}

func (c *diagnosticCloseErrorChannel) Resize(uint16, uint16) error {
	return nil
}

func (c *diagnosticCloseErrorChannel) Close(context.Context) error {
	c.once.Do(func() { close(c.closed) })
	return errors.New("synthetic transport close failure")
}

func (c *diagnosticCloseErrorChannel) LastCloseOutcome() guestssh.CloseOutcome {
	return guestssh.CloseOutcome{Complete: true, Err: errors.New("synthetic transport close failure")}
}

func (c *diagnosticCloseErrorChannel) Wait() (int, error) {
	<-c.closed
	return 0, nil
}

func TestDiagnosticDestructiveDeniedExactRetryReturnsCachedReceipt(t *testing.T) {
	dir, err := os.MkdirTemp("", "amc-task008-retry-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	root := filepath.Join(dir, "state")
	sd, err := statedir.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	mgr := sessions.NewManager(sd.SessionsDir(), diagnosticNeverTransport{}, time.Now)
	svc := app.NewSessionService(
		mgr,
		diagnosticDestructiveSafety{},
		nil,
		audit.NewStore(sd.AuditDir()),
		receipt.NewStore(sd.ReceiptsDir()),
		approval.NewStore(sd.ApprovalsDir()),
	)

	scopes := domain.NewScopeSet(domain.ScopeSessionOpen)
	actor, err := domain.NewActorContext("agent:diagnostic", "agent:diagnostic", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	params := app.SessionOpenParams{
		Target:         "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Caller:         actor,
		Reason:         "diagnose denied retry",
		IdempotencyKey: "idem-diagnostic-denied-retry",
		Timeout:        30 * time.Second,
	}

	_, firstReceipt, firstErr := svc.OpenSession(context.Background(), params)
	if firstErr == nil || firstReceipt == nil {
		t.Fatalf("first call should be denied with a durable receipt: err=%v receipt=%v", firstErr, firstReceipt)
	}

	_, secondReceipt, secondErr := svc.OpenSession(context.Background(), params)
	if secondErr == nil || secondReceipt == nil || secondReceipt.ReceiptID != firstReceipt.ReceiptID {
		t.Fatalf("exact denied retry must return the same cached receipt: err=%v receipt=%v", secondErr, secondReceipt)
	}
}

func TestDiagnosticMutationTimeoutBoundsTransportWithoutCallerDeadline(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	sd, err := statedir.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	transport := &trackingTransport{}
	mgr := sessions.NewManager(sd.SessionsDir(), transport, time.Now)
	svc := app.NewSessionService(
		mgr,
		diagnosticReversibleSafety{},
		nil,
		audit.NewStore(sd.AuditDir()),
		receipt.NewStore(sd.ReceiptsDir()),
		approval.NewStore(sd.ApprovalsDir()),
	)

	scopes := domain.NewScopeSet(domain.ScopeSessionOpen, domain.ScopeSessionRead, domain.ScopeSessionWrite)
	actor, err := domain.NewActorContext("agent:diagnostic", "agent:diagnostic", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	obs, _, err := svc.OpenSession(context.Background(), app.SessionOpenParams{
		Target:         "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Caller:         actor,
		Reason:         "open diagnostic session",
		IdempotencyKey: "idem-diagnostic-timeout-open",
	})
	if err != nil {
		t.Fatal(err)
	}

	transport.writeDelay = 2 * time.Second
	started := time.Now()
	_, rcpt, err := svc.WriteSession(context.Background(), app.SessionWriteParams{
		SessionID:      obs.ID,
		Caller:         actor,
		Data:           "slow write\r\n",
		Reason:         "diagnose timeout",
		IdempotencyKey: "idem-diagnostic-timeout-write",
		Timeout:        500 * time.Millisecond,
	})
	if !errors.Is(err, context.DeadlineExceeded) || rcpt == nil || rcpt.Outcome.Status != domain.OutcomeAborted {
		t.Fatalf("timeout must abort transport and persist an aborted receipt: elapsed=%s err=%v receipt=%v", time.Since(started), err, rcpt)
	}
}

func TestAdmissionDeadlineAfterLeaseLeavesNoDurableMutationState(t *testing.T) {
	sd, err := statedir.Resolve(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	transport := &trackingTransport{}
	safety := &expiringAdmissionSafety{}
	mgr := sessions.NewManager(sd.SessionsDir(), transport, time.Now)
	auditStore := audit.NewStore(sd.AuditDir())
	receiptStore := receipt.NewStore(sd.ReceiptsDir())
	svc := app.NewSessionService(mgr, safety, nil, auditStore, receiptStore, approval.NewStore(sd.ApprovalsDir()))
	scopes := domain.NewScopeSet(domain.ScopeSessionOpen, domain.ScopeSessionRead, domain.ScopeSessionClose)
	actor, err := domain.NewActorContext("agent:deadline", "agent:deadline", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	params := app.SessionOpenParams{
		Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Caller: actor,
		Reason: "expire during admission", IdempotencyKey: "admission-expired", Timeout: 10 * time.Millisecond,
	}
	assertAdmissionDeadlineLeavesNoState(t, svc, transport, auditStore, sd, params)
	assertAdmissionRetrySucceeds(t, svc, transport, params)
	if err := mgr.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func assertAdmissionDeadlineLeavesNoState(t *testing.T, svc *app.SessionService, transport *trackingTransport, auditStore *audit.Store, sd *statedir.StateDir, params app.SessionOpenParams) {
	t.Helper()
	obs, rcpt, err := svc.OpenSession(context.Background(), params)
	if obs != nil || rcpt != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired admission = obs %+v receipt %+v err %v", obs, rcpt, err)
	}
	if got := atomic.LoadInt32(&transport.dialCalls); got != 0 {
		t.Fatalf("transport dial calls after expired admission = %d, want 0", got)
	}
	assertLookupDeadlineLeftNoDurableState(t, sd)
	if events, err := auditStore.Tail(10); err != nil || len(events) != 0 {
		t.Fatalf("audit after expired admission = %v err %v", events, err)
	}
	if entries, err := os.ReadDir(sd.ApprovalsDir()); err != nil || len(entries) != 0 {
		t.Fatalf("approval state after expired admission = %v err %v", entries, err)
	}
}

func assertAdmissionRetrySucceeds(t *testing.T, svc *app.SessionService, transport *trackingTransport, params app.SessionOpenParams) {
	t.Helper()
	params.Timeout = 30 * time.Second
	opened, retryReceipt, err := svc.OpenSession(context.Background(), params)
	if err != nil || opened == nil || retryReceipt == nil {
		t.Fatalf("retry after pre-reservation deadline = session %+v receipt %+v err %v", opened, retryReceipt, err)
	}
	if got := atomic.LoadInt32(&transport.dialCalls); got != 1 {
		t.Fatalf("retry transport dials = %d, want 1", got)
	}
}

func TestDiagnosticReadCursorContinuationDoesNotSkipChunk(t *testing.T) {
	buf := sessions.NewRingBuffer(1024)
	buf.Append("a", time.Now())
	buf.Append("b", time.Now())

	first, nextSeq, _, hasMore := buf.ReadAfter(0, 1)
	if len(first) != 1 || !hasMore {
		t.Fatalf("expected first bounded page with continuation: chunks=%v has_more=%v", first, hasMore)
	}
	second, _, _, _ := buf.ReadAfter(nextSeq, 1)
	if len(second) != 1 || second[0].Seq != 2 {
		t.Fatalf("using next_seq as exclusive after_seq must return the next chunk: next_seq=%d chunks=%v", nextSeq, second)
	}
}

func TestDiagnosticCloseFailureCannotProduceSuccessReceipt(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	sd, err := statedir.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	channel := &diagnosticCloseErrorChannel{closed: make(chan struct{})}
	mgr := sessions.NewManager(sd.SessionsDir(), diagnosticCloseErrorTransport{channel: channel}, time.Now)
	svc := app.NewSessionService(
		mgr,
		diagnosticReversibleSafety{},
		nil,
		audit.NewStore(sd.AuditDir()),
		receipt.NewStore(sd.ReceiptsDir()),
		approval.NewStore(sd.ApprovalsDir()),
	)

	scopes := domain.NewScopeSet(domain.ScopeSessionOpen, domain.ScopeSessionRead, domain.ScopeSessionWrite, domain.ScopeSessionClose)
	actor, err := domain.NewActorContext("agent:diagnostic", "agent:diagnostic", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	obs, _, err := svc.OpenSession(context.Background(), app.SessionOpenParams{
		Target:         "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Caller:         actor,
		Reason:         "open close-failure diagnostic",
		IdempotencyKey: "idem-diagnostic-close-open",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, rcpt, err := svc.CloseSession(context.Background(), app.SessionCloseParams{
		SessionID:      obs.ID,
		Caller:         actor,
		Reason:         "diagnose close failure",
		IdempotencyKey: "idem-diagnostic-close",
		Timeout:        time.Second,
	})
	if err == nil || rcpt == nil || rcpt.Outcome.Status == domain.OutcomeSuccess {
		t.Fatalf("transport close failure must be surfaced with a non-success receipt: err=%v receipt=%v", err, rcpt)
	}
}

func TestDiagnosticAgentCannotSubmitRawApproval(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	sd, err := statedir.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatal(err)
	}

	transport := &trackingTransport{}
	fixedTime := time.Date(2026, 8, 30, 3, 0, 0, 0, time.UTC)
	mgr := sessions.NewManager(sd.SessionsDir(), transport, func() time.Time { return fixedTime })
	svc := app.NewSessionService(
		mgr,
		diagnosticDestructiveSafety{},
		nil,
		audit.NewStore(sd.AuditDir()),
		receipt.NewStore(sd.ReceiptsDir()),
		approval.NewStore(sd.ApprovalsDir()),
		app.WithSessionClock(func() time.Time { return fixedTime }),
	)

	scopes := domain.NewScopeSet(domain.ScopeSessionOpen)
	actor, err := domain.NewActorContext("agent:mcp-local", "agent:mcp-local", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	const target = "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	const idempotencyKey = "idem-diagnostic-agent-approval"
	op := domain.Operation{
		Kind:                "session.open",
		Target:              domain.MachineRef(target),
		Actor:               actor,
		Reason:              "diagnose agent approval injection",
		Deadline:            fixedTime.Add(30 * time.Second),
		IdempotencyKey:      idempotencyKey,
		RequiredCapability:  domain.CapabilitySessionOpen,
		RequiredScopes:      []string{domain.ScopeSessionOpen},
		Classification:      domain.ClassDestructivePrivileged,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters: map[string]any{
			"cols": uint16(domain.DefaultCols),
			"rows": uint16(domain.DefaultRows),
			"term": domain.DefaultTermType,
		},
	}
	fingerprint, err := op.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	rawApproval := &domain.Approval{
		ID:              "app-0123456789abcdef0123456789abcdef",
		Actor:           actor.EffectiveActor,
		Target:          domain.MachineRef(target),
		AuthorizedClass: domain.ClassDestructivePrivileged,
		Fingerprint:     fingerprint,
		IdempotencyKey:  idempotencyKey,
		IssuedAt:        fixedTime.Add(-time.Minute),
		ExpiresAt:       fixedTime.Add(time.Minute),
	}

	_, rcpt, err := svc.OpenSession(context.Background(), app.SessionOpenParams{
		Target:         target,
		Caller:         actor,
		Reason:         op.Reason,
		IdempotencyKey: idempotencyKey,
		Timeout:        30 * time.Second,
		Approval:       rawApproval,
	})
	if err == nil || rcpt == nil || rcpt.Outcome.Status != domain.OutcomeDenied || transport.dialCalls != 0 {
		t.Fatalf("non-admin agent-supplied approval must be denied before transport: err=%v receipt=%v dial_calls=%d", err, rcpt, transport.dialCalls)
	}
}
