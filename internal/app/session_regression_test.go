package app_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/policy"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/sessions"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

// trackingTransport records every transport call and allows simulating failure or redial panic.
type trackingTransport struct {
	dialCalls    int32
	writeCalls   int32
	controlCalls int32
	closeCalls   int32
	panicOnDial  bool
	failWrite    bool
	writeDelay   time.Duration
}

func (t *trackingTransport) Dial(_ context.Context, _ domain.MachineRef, _, _ uint16, _ string) (guestssh.Channel, error) {
	if t.panicOnDial {
		panic("transport.Dial called unexpectedly during cached retry or restart reconstruction")
	}
	atomic.AddInt32(&t.dialCalls, 1)
	return &trackingChannel{
		parent: t,
		waitCh: make(chan struct{}),
	}, nil
}

type trackingChannel struct {
	parent *trackingTransport
	pr     *io.PipeReader
	pw     *io.PipeWriter
	closed bool
	waitCh chan struct{}
	mu     sync.Mutex
}

func (c *trackingChannel) Read(p []byte) (int, error) {
	if c.pr == nil {
		c.pr, c.pw = io.Pipe()
		go func() {
			_, _ = c.pw.Write([]byte("PS C:\\> "))
		}()
	}
	return c.pr.Read(p)
}

func (c *trackingChannel) Write(ctx context.Context, p []byte) (int, error) {
	atomic.AddInt32(&c.parent.writeCalls, 1)
	if c.parent.writeDelay > 0 {
		select {
		case <-time.After(c.parent.writeDelay):
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	if c.parent.failWrite {
		return 0, errors.New("simulated transport write failure")
	}
	return len(p), nil
}

func (c *trackingChannel) SendControl(_ context.Context, _ domain.ControlKey) error {
	atomic.AddInt32(&c.parent.controlCalls, 1)
	return nil
}

func (c *trackingChannel) Resize(_, _ uint16) error {
	return nil
}

func (c *trackingChannel) Close(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	atomic.AddInt32(&c.parent.closeCalls, 1)
	close(c.waitCh)
	if c.pw != nil {
		_ = c.pw.Close()
	}
	return nil
}

func (c *trackingChannel) Wait() (int, error) {
	<-c.waitCh
	return 0, nil
}

type testRetryParams struct {
	openParams  app.SessionOpenParams
	writeParams app.SessionWriteParams
	ctrlParams  app.SessionControlParams
	closeParams app.SessionCloseParams
}

func verifyExactRetryOpen(ctx context.Context, t *testing.T, svc *app.SessionService, transport *trackingTransport, params app.SessionOpenParams) (*domain.SessionObservation, *domain.Receipt) {
	t.Helper()
	obs1, rcpt1, err := svc.OpenSession(ctx, params)
	if err != nil || obs1 == nil || rcpt1 == nil {
		t.Fatalf("OpenSession 1 failed: %v", err)
	}
	if atomic.LoadInt32(&transport.dialCalls) != 1 {
		t.Fatalf("expected 1 dial call, got %d", transport.dialCalls)
	}

	obsRetry, rcptRetry, err := svc.OpenSession(ctx, params)
	if err != nil || rcptRetry == nil {
		t.Fatalf("OpenSession retry failed: %v", err)
	}
	if atomic.LoadInt32(&transport.dialCalls) != 1 {
		t.Errorf("expected still 1 dial call on retry, got %d", transport.dialCalls)
	}
	if rcpt1.ReceiptID != rcptRetry.ReceiptID {
		t.Errorf("receipt ID changed on retry: %s vs %s", rcpt1.ReceiptID, rcptRetry.ReceiptID)
	}
	if obsRetry.ID != obs1.ID {
		t.Errorf("session ID changed on retry: %s vs %s", obs1.ID, obsRetry.ID)
	}
	return obs1, rcpt1
}

func verifyExactRetryWrite(ctx context.Context, t *testing.T, svc *app.SessionService, transport *trackingTransport, p app.SessionWriteParams) *domain.Receipt {
	t.Helper()
	wn1, rcptWrite1, err := svc.WriteSession(ctx, p)
	if err != nil || wn1 != len(p.Data) || rcptWrite1 == nil {
		t.Fatalf("WriteSession 1 failed: %v", err)
	}
	if atomic.LoadInt32(&transport.writeCalls) != 1 {
		t.Fatalf("expected 1 write call, got %d", transport.writeCalls)
	}

	wnRetry, rcptWriteRetry, err := svc.WriteSession(ctx, p)
	if err != nil || rcptWriteRetry == nil || wnRetry != wn1 || rcptWrite1.ReceiptID != rcptWriteRetry.ReceiptID {
		t.Fatalf("WriteSession retry unexpected: n=%d vs %d, err=%v", wnRetry, wn1, err)
	}
	if atomic.LoadInt32(&transport.writeCalls) != 1 {
		t.Errorf("expected still 1 write call on retry, got %d", transport.writeCalls)
	}
	return rcptWrite1
}

func verifyExactRetryControl(ctx context.Context, t *testing.T, svc *app.SessionService, transport *trackingTransport, p app.SessionControlParams) *domain.Receipt {
	t.Helper()
	rcptCtrl1, err := svc.ControlSession(ctx, p)
	if err != nil || rcptCtrl1 == nil {
		t.Fatalf("ControlSession 1 failed: %v", err)
	}
	if atomic.LoadInt32(&transport.controlCalls) != 1 {
		t.Fatalf("expected 1 control call, got %d", transport.controlCalls)
	}

	rcptCtrlRetry, err := svc.ControlSession(ctx, p)
	if err != nil || rcptCtrlRetry == nil || rcptCtrl1.ReceiptID != rcptCtrlRetry.ReceiptID {
		t.Fatalf("ControlSession retry unexpected: %v", err)
	}
	if atomic.LoadInt32(&transport.controlCalls) != 1 {
		t.Errorf("expected still 1 control call on retry, got %d", transport.controlCalls)
	}
	return rcptCtrl1
}

func verifyExactRetryClose(ctx context.Context, t *testing.T, svc *app.SessionService, transport *trackingTransport, params app.SessionCloseParams) *domain.Receipt {
	t.Helper()
	obsClose1, rcptClose1, err := svc.CloseSession(ctx, params)
	if err != nil || obsClose1 == nil || rcptClose1 == nil {
		t.Fatalf("CloseSession 1 failed: %v", err)
	}
	if atomic.LoadInt32(&transport.closeCalls) != 1 {
		t.Fatalf("expected 1 close call, got %d", transport.closeCalls)
	}

	obsCloseRetry, rcptCloseRetry, err := svc.CloseSession(ctx, params)
	if err != nil || rcptCloseRetry == nil {
		t.Fatalf("CloseSession retry failed: %v", err)
	}
	if atomic.LoadInt32(&transport.closeCalls) != 1 {
		t.Errorf("expected still 1 close call on retry, got %d", transport.closeCalls)
	}
	if rcptClose1.ReceiptID != rcptCloseRetry.ReceiptID {
		t.Errorf("receipt ID changed on close retry")
	}
	if obsCloseRetry.State != domain.SessionStateClosed {
		t.Errorf("expected closed observation on close retry, got %s", obsCloseRetry.State)
	}
	return rcptClose1
}

func verifyRestartReconstructionOpenWrite(ctx context.Context, t *testing.T, newSvc *app.SessionService, p testRetryParams, rcptOpen, rcptWrite *domain.Receipt) {
	t.Helper()
	postOpenObs, postOpenRcpt, err := newSvc.OpenSession(ctx, p.openParams)
	if err != nil || postOpenRcpt == nil || postOpenObs == nil || postOpenRcpt.ReceiptID != rcptOpen.ReceiptID {
		t.Fatalf("OpenSession after restart failed: %v", err)
	}

	postWriteN, postWriteRcpt, err := newSvc.WriteSession(ctx, p.writeParams)
	if err != nil || postWriteRcpt == nil || postWriteN != len(p.writeParams.Data) || postWriteRcpt.ReceiptID != rcptWrite.ReceiptID {
		t.Fatalf("WriteSession after restart failed: %v", err)
	}
}

func verifyRestartReconstructionControlClose(ctx context.Context, t *testing.T, newSvc *app.SessionService, p testRetryParams, rcptCtrl, rcptClose *domain.Receipt) {
	t.Helper()
	postCtrlRcpt, err := newSvc.ControlSession(ctx, p.ctrlParams)
	if err != nil || postCtrlRcpt == nil || postCtrlRcpt.ReceiptID != rcptCtrl.ReceiptID {
		t.Fatalf("ControlSession after restart failed: %v", err)
	}

	postCloseObs, postCloseRcpt, err := newSvc.CloseSession(ctx, p.closeParams)
	if err != nil || postCloseRcpt == nil || postCloseObs == nil || postCloseRcpt.ReceiptID != rcptClose.ReceiptID || postCloseObs.State != domain.SessionStateClosed {
		t.Fatalf("CloseSession after restart failed: %v", err)
	}
}

func verifyRestartReconstruction(ctx context.Context, t *testing.T, sd *statedir.StateDir, safetyRes app.SafetyResolver, p testRetryParams, rcptOpen, rcptWrite, rcptCtrl, rcptClose *domain.Receipt) {
	t.Helper()
	panicTransport := &trackingTransport{panicOnDial: true}
	newSessionMgr := sessions.NewManager(sd.SessionsDir(), panicTransport, time.Now)
	newReceiptStore := receipt.NewStore(sd.ReceiptsDir())
	newAuditStore := audit.NewStore(sd.AuditDir())
	newApprovalStore := approval.NewStore(sd.ApprovalsDir())
	newLeaseMgr := lease.NewManager(sd.LeasesDir(), lease.WithLivenessChecker(&lease.DefaultLivenessChecker{}))

	newSvc := app.NewSessionService(newSessionMgr, safetyRes, newLeaseMgr, newAuditStore, newReceiptStore, newApprovalStore)
	verifyRestartReconstructionOpenWrite(ctx, t, newSvc, p, rcptOpen, rcptWrite)
	verifyRestartReconstructionControlClose(ctx, t, newSvc, p, rcptCtrl, rcptClose)
}

// Regression Test 1 & 2: Exact cached retry makes zero second transport effects,
// and survives service/manager restart without any transport calls.
func TestSessionRegression_ExactRetryZeroEffectsAndRestartSurvives(t *testing.T) {
	tempDir := t.TempDir()
	sd, _ := statedir.Resolve(tempDir)
	_ = sd.EnsureDirs()

	transport := &trackingTransport{}
	sessionMgr := sessions.NewManager(sd.SessionsDir(), transport, time.Now)
	auditStore := audit.NewStore(sd.AuditDir())
	receiptStore := receipt.NewStore(sd.ReceiptsDir())
	approvalStore := approval.NewStore(sd.ApprovalsDir())
	leaseMgr := lease.NewManager(sd.LeasesDir(), lease.WithLivenessChecker(&lease.DefaultLivenessChecker{}))

	safetyRes := &mockSafetyResolver{
		resolution: app.SafetyResolution{
			Classification: domain.ClassReversibleMutation,
			Contained:      true,
			RollbackRef:    "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
			RollbackState: policy.RollbackState{
				Available:    true,
				Verified:     true,
				CheckpointID: "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
			},
		},
	}

	svc := app.NewSessionService(sessionMgr, safetyRes, leaseMgr, auditStore, receiptStore, approvalStore)
	ctx := context.Background()

	scopes := domain.NewScopeSet(domain.ScopeSessionOpen, domain.ScopeSessionRead, domain.ScopeSessionWrite, domain.ScopeSessionClose, "evidence:sensitive")
	actor, _ := domain.NewActorContext("agent:primary", "agent:primary", scopes, scopes)
	target := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	openParams := app.SessionOpenParams{
		Target:         target,
		Caller:         actor,
		Reason:         "open session",
		IdempotencyKey: "idem-r-open",
		Cols:           80,
		Rows:           24,
	}

	obs, rcptOpen := verifyExactRetryOpen(ctx, t, svc, transport, openParams)

	retryParams := testRetryParams{
		openParams: openParams,
		writeParams: app.SessionWriteParams{
			SessionID:      obs.ID,
			Caller:         actor,
			Data:           "Get-Process\r\n",
			Reason:         "check processes",
			IdempotencyKey: "idem-r-write",
		},
		ctrlParams: app.SessionControlParams{
			SessionID:      obs.ID,
			Caller:         actor,
			Key:            domain.ControlKeyCtrlC,
			Reason:         "interrupt",
			IdempotencyKey: "idem-r-ctrl",
		},
		closeParams: app.SessionCloseParams{
			SessionID:      obs.ID,
			Caller:         actor,
			Reason:         "done",
			IdempotencyKey: "idem-r-close",
		},
	}

	rcptWrite := verifyExactRetryWrite(ctx, t, svc, transport, retryParams.writeParams)
	rcptCtrl := verifyExactRetryControl(ctx, t, svc, transport, retryParams.ctrlParams)
	rcptClose := verifyExactRetryClose(ctx, t, svc, transport, retryParams.closeParams)
	verifyRestartReconstruction(ctx, t, sd, safetyRes, retryParams, rcptOpen, rcptWrite, rcptCtrl, rcptClose)
}

// Regression Test 3: Parameter and cross-actor collisions fail closed without disclosing cached data.
func TestSessionRegression_CollisionsFailClosedAndNoDataLeak(t *testing.T) {
	tempDir := t.TempDir()
	sd, _ := statedir.Resolve(tempDir)
	_ = sd.EnsureDirs()

	transport := &trackingTransport{}
	sessionMgr := sessions.NewManager(sd.SessionsDir(), transport, time.Now)
	auditStore := audit.NewStore(sd.AuditDir())
	receiptStore := receipt.NewStore(sd.ReceiptsDir())
	approvalStore := approval.NewStore(sd.ApprovalsDir())
	leaseMgr := lease.NewManager(sd.LeasesDir(), lease.WithLivenessChecker(&lease.DefaultLivenessChecker{}))

	safetyRes := &mockSafetyResolver{
		resolution: app.SafetyResolution{
			Classification: domain.ClassReversibleMutation,
			Contained:      true,
			RollbackRef:    "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
			RollbackState: policy.RollbackState{
				Available:    true,
				Verified:     true,
				CheckpointID: "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
			},
		},
	}

	svc := app.NewSessionService(sessionMgr, safetyRes, leaseMgr, auditStore, receiptStore, approvalStore)
	ctx := context.Background()

	scopes := domain.NewScopeSet(domain.ScopeSessionOpen, domain.ScopeSessionRead, domain.ScopeSessionWrite, domain.ScopeSessionClose, "evidence:sensitive")
	actorA, _ := domain.NewActorContext("agent:actor-A", "agent:actor-A", scopes, scopes)
	actorB, _ := domain.NewActorContext("agent:actor-B", "agent:actor-B", scopes, scopes)
	target := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	openParamsA := app.SessionOpenParams{
		Target:         target,
		Caller:         actorA,
		Reason:         "open session A",
		IdempotencyKey: "idem-collision-test",
		Cols:           80,
		Rows:           24,
	}
	obsA, rcptA, err := svc.OpenSession(ctx, openParamsA)
	if err != nil || obsA == nil || rcptA == nil {
		t.Fatalf("OpenSession Actor A failed: %v", err)
	}

	// Cross-actor collision: Actor B attempts open with Actor A's idempotency key
	openParamsB := openParamsA
	openParamsB.Caller = actorB
	obsB, rcptB, err := svc.OpenSession(ctx, openParamsB)
	if err == nil {
		t.Fatal("expected collision error for cross-actor idempotency key reuse")
	}
	if obsB != nil || rcptB != nil {
		t.Errorf("expected nil observation and receipt on cross-actor collision")
	}

	// Parameter collision: Actor A attempts open with same idempotency key but modified dimensions
	openParamsAChanged := openParamsA
	openParamsAChanged.Cols = 120
	obsAChanged, rcptAChanged, err := svc.OpenSession(ctx, openParamsAChanged)
	if err == nil {
		t.Fatal("expected collision error for parameter change on same idempotency key")
	}
	if obsAChanged != nil || rcptAChanged != nil {
		t.Errorf("expected nil observation and receipt on parameter collision")
	}
}

func checkDiskFilesForLeaks(t *testing.T, dir, secret string, checkChkSession bool) {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("failed to read file %s: %v", f, err)
		}
		if strings.Contains(string(data), secret) {
			t.Errorf("SECURITY VIOLATION: secret found in file %s: %s", f, string(data))
		}
		if checkChkSession && strings.Contains(string(data), "chk-session") {
			t.Errorf("FABRICATION VIOLATION: fabricated chk-session ref found in %s", f)
		}
	}
}

// Regression Test 7 & 8: Failures/timeouts produce terminal receipts/audit,
// and raw session input is never stored in operation parameters, audit, or receipts.
func TestSessionRegression_AuditReceiptIntegrityAndRedaction(t *testing.T) {
	tempDir := t.TempDir()
	sd, _ := statedir.Resolve(tempDir)
	_ = sd.EnsureDirs()

	transport := &trackingTransport{}
	sessionMgr := sessions.NewManager(sd.SessionsDir(), transport, time.Now)
	auditStore := audit.NewStore(sd.AuditDir())
	receiptStore := receipt.NewStore(sd.ReceiptsDir())
	approvalStore := approval.NewStore(sd.ApprovalsDir())
	leaseMgr := lease.NewManager(sd.LeasesDir(), lease.WithLivenessChecker(&lease.DefaultLivenessChecker{}))

	safetyRes := &mockSafetyResolver{
		resolution: app.SafetyResolution{
			Classification: domain.ClassReversibleMutation,
			Contained:      true,
			RollbackRef:    "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
			RollbackState: policy.RollbackState{
				Available:    true,
				Verified:     true,
				CheckpointID: "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
			},
		},
	}

	svc := app.NewSessionService(sessionMgr, safetyRes, leaseMgr, auditStore, receiptStore, approvalStore)
	ctx := context.Background()

	scopes := domain.NewScopeSet(domain.ScopeSessionOpen, domain.ScopeSessionRead, domain.ScopeSessionWrite, domain.ScopeSessionClose, "evidence:sensitive")
	actor, _ := domain.NewActorContext("agent:test", "agent:test", scopes, scopes)
	target := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	obs, _, err := svc.OpenSession(ctx, app.SessionOpenParams{
		Target:         target,
		Caller:         actor,
		Reason:         "open session",
		IdempotencyKey: "idem-redact-open",
	})
	if err != nil {
		t.Fatalf("OpenSession failed: %v", err)
	}

	secretCommand := "SECRET_CREDENTIAL_KEY_99999_SUPER_CONFIDENTIAL\r\n"
	wn, rcptWrite, err := svc.WriteSession(ctx, app.SessionWriteParams{
		SessionID:      obs.ID,
		Caller:         actor,
		Data:           secretCommand,
		Reason:         "send command",
		IdempotencyKey: "idem-redact-write",
	})
	if err != nil || wn != len(secretCommand) || rcptWrite == nil {
		t.Fatalf("WriteSession failed: %v", err)
	}
	if rcptWrite.RedactionStatus != domain.RedactionApplied {
		t.Errorf("expected RedactionApplied on write receipt, got %s", rcptWrite.RedactionStatus)
	}

	checkDiskFilesForLeaks(t, sd.ReceiptsDir(), "SECRET_CREDENTIAL_KEY", true)
	checkDiskFilesForLeaks(t, sd.AuditDir(), "SECRET_CREDENTIAL_KEY", false)

	transport.failWrite = true
	_, failRcpt, err := svc.WriteSession(ctx, app.SessionWriteParams{
		SessionID:      obs.ID,
		Caller:         actor,
		Data:           "failing command\r\n",
		Reason:         "failing write",
		IdempotencyKey: "idem-fail-write",
	})
	if err == nil || failRcpt == nil || failRcpt.Outcome.Status != domain.OutcomeFailed {
		t.Errorf("expected failed outcome receipt for transport error, got %+v", failRcpt)
	}
}

// Regression Test 9: Timeout / cancellation causes no late write effects.
func TestSessionRegression_CancellationAndTimeout(t *testing.T) {
	tempDir := t.TempDir()
	sd, _ := statedir.Resolve(tempDir)
	_ = sd.EnsureDirs()

	transport := &trackingTransport{
		writeDelay: 200 * time.Millisecond,
	}
	sessionMgr := sessions.NewManager(sd.SessionsDir(), transport, time.Now)
	auditStore := audit.NewStore(sd.AuditDir())
	receiptStore := receipt.NewStore(sd.ReceiptsDir())
	approvalStore := approval.NewStore(sd.ApprovalsDir())
	leaseMgr := lease.NewManager(sd.LeasesDir(), lease.WithLivenessChecker(&lease.DefaultLivenessChecker{}))

	safetyRes := &mockSafetyResolver{
		resolution: app.SafetyResolution{
			Classification: domain.ClassReversibleMutation,
			Contained:      true,
			RollbackRef:    "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
			RollbackState: policy.RollbackState{
				Available:    true,
				Verified:     true,
				CheckpointID: "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
			},
		},
	}

	svc := app.NewSessionService(sessionMgr, safetyRes, leaseMgr, auditStore, receiptStore, approvalStore)

	scopes := domain.NewScopeSet(domain.ScopeSessionOpen, domain.ScopeSessionRead, domain.ScopeSessionWrite, domain.ScopeSessionClose, "evidence:sensitive")
	actor, _ := domain.NewActorContext("agent:test", "agent:test", scopes, scopes)
	target := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	obs, _, err := svc.OpenSession(context.Background(), app.SessionOpenParams{
		Target:         target,
		Caller:         actor,
		Reason:         "open session",
		IdempotencyKey: "idem-timeout-open",
	})
	if err != nil {
		t.Fatalf("OpenSession failed: %v", err)
	}

	ctxTimeout, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, abortRcpt, err := svc.WriteSession(ctxTimeout, app.SessionWriteParams{
		SessionID:      obs.ID,
		Caller:         actor,
		Data:           "stalled write\r\n",
		Reason:         "timeout write",
		IdempotencyKey: "idem-timeout-write",
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if abortRcpt == nil || abortRcpt.Outcome.Status != domain.OutcomeAborted {
		t.Errorf("expected aborted outcome receipt for timeout, got %+v", abortRcpt)
	}
}

// Regression Test 10: Scope authorization on read/wait/list/get.
func TestSessionRegression_ScopeEnforcement(t *testing.T) {
	tempDir := t.TempDir()
	sd, _ := statedir.Resolve(tempDir)
	_ = sd.EnsureDirs()

	transport := &trackingTransport{}
	sessionMgr := sessions.NewManager(sd.SessionsDir(), transport, time.Now)
	auditStore := audit.NewStore(sd.AuditDir())
	receiptStore := receipt.NewStore(sd.ReceiptsDir())
	approvalStore := approval.NewStore(sd.ApprovalsDir())
	leaseMgr := lease.NewManager(sd.LeasesDir(), lease.WithLivenessChecker(&lease.DefaultLivenessChecker{}))

	safetyRes := &mockSafetyResolver{
		resolution: app.SafetyResolution{
			Classification: domain.ClassReversibleMutation,
			Contained:      true,
			RollbackRef:    "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
			RollbackState: policy.RollbackState{
				Available:    true,
				Verified:     true,
				CheckpointID: "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
			},
		},
	}

	svc := app.NewSessionService(sessionMgr, safetyRes, leaseMgr, auditStore, receiptStore, approvalStore)
	ctx := context.Background()

	fullScopes := domain.NewScopeSet(domain.ScopeSessionOpen, domain.ScopeSessionRead, domain.ScopeSessionWrite, domain.ScopeSessionClose, "evidence:sensitive")
	fullActor, _ := domain.NewActorContext("agent:admin", "agent:admin", fullScopes, fullScopes)
	target := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	obs, _, err := svc.OpenSession(ctx, app.SessionOpenParams{
		Target:         target,
		Caller:         fullActor,
		Reason:         "open session",
		IdempotencyKey: "idem-scopes-open",
	})
	if err != nil {
		t.Fatalf("OpenSession failed: %v", err)
	}

	noEvidenceScopes := domain.NewScopeSet(domain.ScopeSessionRead)
	noEvidenceActor, _ := domain.NewActorContext("agent:admin", "agent:admin", noEvidenceScopes, noEvidenceScopes)

	if _, _, _, _, _, err := svc.ReadSession(ctx, obs.ID, noEvidenceActor, 0, 1024); !errors.Is(err, domain.ErrSessionAccessDenied) {
		t.Errorf("expected ErrSessionAccessDenied for ReadSession without sensitive evidence scope, got: %v", err)
	}
	if _, _, _, _, _, err := svc.WaitSession(ctx, obs.ID, noEvidenceActor, 50*time.Millisecond, "", 0, time.Second); !errors.Is(err, domain.ErrSessionAccessDenied) {
		t.Errorf("expected ErrSessionAccessDenied for WaitSession without sensitive evidence scope, got: %v", err)
	}

	noReadScopes := domain.NewScopeSet(domain.ScopeSessionWrite)
	noReadActor, _ := domain.NewActorContext("agent:admin", "agent:admin", noReadScopes, noReadScopes)

	if _, err := svc.ListSessions(ctx, noReadActor, ""); !errors.Is(err, domain.ErrSessionAccessDenied) {
		t.Errorf("expected ErrSessionAccessDenied on ListSessions, got: %v", err)
	}
	if _, err := svc.GetSession(ctx, obs.ID, noReadActor); !errors.Is(err, domain.ErrSessionAccessDenied) {
		t.Errorf("expected ErrSessionAccessDenied on GetSession, got: %v", err)
	}
}
