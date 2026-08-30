package app_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
	"github.com/Horcag/agent-machine-control/internal/guest/ssh/fakeserver"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/policy"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/sessions"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	gossh "golang.org/x/crypto/ssh"
)

type mockSafetyResolver struct {
	resolution app.SafetyResolution
	err        error
}

func (r *mockSafetyResolver) ResolveSafety(_ context.Context, _ domain.MachineRef) (app.SafetyResolution, error) {
	return r.resolution, r.err
}

type testSessionHarness struct {
	stateDir       string
	server         *fakeserver.FakeSSHServer
	sessionMgr     *sessions.Manager
	auditStore     *audit.Store
	receiptStore   *receipt.Store
	approvalStore  *approval.Store
	leaseMgr       *lease.Manager
	safetyResolver *mockSafetyResolver
	svc            *app.SessionService
	agentCaller    domain.ActorContext
	target         string
}

func setupSessionServiceTest(t *testing.T) *testSessionHarness {
	tempDir := t.TempDir()
	sd, err := statedir.Resolve(tempDir)
	if err != nil {
		t.Fatalf("failed to resolve statedir: %v", err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatalf("failed to ensure dirs: %v", err)
	}

	server, err := fakeserver.New(fakeserver.ModeEcho, nil)
	if err != nil {
		t.Fatalf("failed to start fake ssh server: %v", err)
	}

	target := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	auditStore := audit.NewStore(sd.AuditDir())
	receiptStore := receipt.NewStore(sd.ReceiptsDir())
	approvalStore := approval.NewStore(sd.ApprovalsDir())
	leaseMgr := lease.NewManager(sd.LeasesDir(), lease.WithLivenessChecker(&lease.DefaultLivenessChecker{}))

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate ed25519 key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("failed to create signer: %v", err)
	}

	kp := &guestssh.MockKeyProvider{
		Signer:          signer,
		PinnedKeySHA256: server.HostKeyPin(),
		Endpoint:        server.Addr(),
		User:            "testadmin",
	}
	transport := guestssh.NewTransport(kp)

	mgr := sessions.NewManager(sd.SessionsDir(), transport, nil)

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

	fixedTime := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	svc := app.NewSessionService(mgr, safetyRes, leaseMgr, auditStore, receiptStore, approvalStore, app.WithSessionClock(func() time.Time {
		return fixedTime
	}))

	agentPerms := domain.NewScopeSet(
		domain.ScopeSessionOpen,
		domain.ScopeSessionRead,
		domain.ScopeSessionWrite,
		domain.ScopeSessionClose,
		domain.ScopeSessionAdmin,
		"evidence:sensitive",
	)
	agentCaller := domain.ActorContext{
		AuthenticatedCaller:  "agent-builder",
		EffectiveActor:       "agent-builder",
		CallerPermissions:    agentPerms,
		EffectivePermissions: agentPerms,
	}

	return &testSessionHarness{
		stateDir:       tempDir,
		server:         server,
		sessionMgr:     mgr,
		auditStore:     auditStore,
		receiptStore:   receiptStore,
		approvalStore:  approvalStore,
		leaseMgr:       leaseMgr,
		safetyResolver: safetyRes,
		svc:            svc,
		agentCaller:    agentCaller,
		target:         target,
	}
}

func testLifecycleOpenAndRead(ctx context.Context, t *testing.T, h *testSessionHarness) (*domain.SessionObservation, *domain.Receipt, uint64) {
	openParams := app.SessionOpenParams{
		Target:         h.target,
		Caller:         h.agentCaller,
		Reason:         "open terminal for automation",
		IdempotencyKey: "idem-open-full-1",
		Timeout:        30 * time.Second,
		Cols:           80,
		Rows:           24,
		Term:           "xterm-256color",
	}

	obs, openRcpt, err := h.svc.OpenSession(ctx, openParams)
	if err != nil || openRcpt == nil {
		t.Fatalf("OpenSession failed: %v", err)
	}
	if openRcpt.Outcome.Status != domain.OutcomeSuccess || openRcpt.OperationKind != "session.open" {
		t.Errorf("unexpected open receipt status/kind: %v", openRcpt)
	}
	if openRcpt.RedactionStatus != domain.RedactionApplied {
		t.Errorf("expected RedactionApplied on session receipt")
	}

	time.Sleep(50 * time.Millisecond)
	chunks, nextSeq, _, _, _, err := h.svc.ReadSession(ctx, obs.ID, h.agentCaller, 0, 1024)
	if err != nil || len(chunks) == 0 {
		t.Fatalf("ReadSession failed: %v", err)
	}

	return obs, openRcpt, nextSeq
}

func testLifecycleWriteAndControl(ctx context.Context, t *testing.T, h *testSessionHarness, obs *domain.SessionObservation, nextSeq uint64) (*domain.Receipt, *domain.Receipt) {
	writeParams := app.SessionWriteParams{
		SessionID:      obs.ID,
		Caller:         h.agentCaller,
		Data:           "dir\r\n",
		Reason:         "run directory listing",
		IdempotencyKey: "idem-write-full-1",
		Timeout:        30 * time.Second,
	}

	wn, writeRcpt, err := h.svc.WriteSession(ctx, writeParams)
	if err != nil || wn == 0 || writeRcpt == nil {
		t.Fatalf("WriteSession failed: %v", err)
	}

	ctrlParams := app.SessionControlParams{
		SessionID:      obs.ID,
		Caller:         h.agentCaller,
		Key:            domain.ControlKeyCtrlC,
		Reason:         "interrupt previous command",
		IdempotencyKey: "idem-ctrl-full-1",
		Timeout:        30 * time.Second,
	}

	ctrlRcpt, err := h.svc.ControlSession(ctx, ctrlParams)
	if err != nil || ctrlRcpt == nil {
		t.Fatalf("ControlSession failed: %v", err)
	}

	_, _, _, _, _, err = h.svc.WaitSession(ctx, obs.ID, h.agentCaller, 50*time.Millisecond, "", nextSeq, 2*time.Second)
	if err != nil {
		t.Fatalf("WaitSession failed: %v", err)
	}

	return writeRcpt, ctrlRcpt
}

func testLifecycleCloseAndVerify(ctx context.Context, t *testing.T, h *testSessionHarness, obs *domain.SessionObservation, allReceipts []*domain.Receipt) {
	closeParams := app.SessionCloseParams{
		SessionID:      obs.ID,
		Caller:         h.agentCaller,
		Reason:         "work completed",
		IdempotencyKey: "idem-close-full-1",
		Timeout:        30 * time.Second,
		Force:          false,
	}

	closedObs, closeRcpt, err := h.svc.CloseSession(ctx, closeParams)
	if err != nil || closedObs.State != domain.SessionStateClosed || closeRcpt == nil {
		t.Fatalf("CloseSession failed: %v", err)
	}

	allReceipts = append(allReceipts, closeRcpt)
	for _, r := range allReceipts {
		fetched, err := h.receiptStore.Get(string(r.ReceiptID))
		if err != nil || fetched == nil {
			t.Errorf("receipt %s not found in store: %v", r.ReceiptID, err)
		}
	}
}

func TestSessionService_FullLifecycleWithReceipts(t *testing.T) {
	h := setupSessionServiceTest(t)
	defer h.server.Close()
	ctx := context.Background()

	obs, openRcpt, nextSeq := testLifecycleOpenAndRead(ctx, t, h)
	writeRcpt, ctrlRcpt := testLifecycleWriteAndControl(ctx, t, h, obs, nextSeq)
	testLifecycleCloseAndVerify(ctx, t, h, obs, []*domain.Receipt{openRcpt, writeRcpt, ctrlRcpt})
}

func TestSessionService_IdempotencyExactRetry(t *testing.T) {
	h := setupSessionServiceTest(t)
	defer h.server.Close()
	ctx := context.Background()

	openParams := app.SessionOpenParams{
		Target:         h.target,
		Caller:         h.agentCaller,
		Reason:         "initial open",
		IdempotencyKey: "idem-retry-1",
		Timeout:        30 * time.Second,
		Cols:           80,
		Rows:           24,
	}

	obs1, rcpt1, err := h.svc.OpenSession(ctx, openParams)
	if err != nil || obs1 == nil || rcpt1 == nil {
		t.Fatalf("OpenSession 1 failed: %v", err)
	}

	// Exact retry returns same receipt and session observation without transport call
	obsRetry, rcpt2, err := h.svc.OpenSession(ctx, openParams)
	if err != nil || rcpt2 == nil {
		t.Fatalf("OpenSession retry failed: %v", err)
	}
	if rcpt1.ReceiptID != rcpt2.ReceiptID {
		t.Errorf("expected same receipt ID on exact retry, got %s vs %s", rcpt1.ReceiptID, rcpt2.ReceiptID)
	}
	if obsRetry.ID != obs1.ID {
		t.Errorf("expected same session ID %s, got %s", obs1.ID, obsRetry.ID)
	}

	// Write exact retry
	writeParams := app.SessionWriteParams{
		SessionID:      obs1.ID,
		Caller:         h.agentCaller,
		Data:           "hostname\r\n",
		Reason:         "get hostname",
		IdempotencyKey: "idem-write-1",
		Timeout:        30 * time.Second,
	}

	n1, wRcpt1, err := h.svc.WriteSession(ctx, writeParams)
	if err != nil || wRcpt1 == nil {
		t.Fatalf("WriteSession 1 failed: %v", err)
	}

	n2, wRcpt2, err := h.svc.WriteSession(ctx, writeParams)
	if err != nil || wRcpt2 == nil {
		t.Fatalf("WriteSession retry failed: %v", err)
	}
	if wRcpt1.ReceiptID != wRcpt2.ReceiptID {
		t.Errorf("expected same write receipt ID on retry")
	}
	if n1 != n2 || n2 != len("hostname\r\n") {
		t.Errorf("expected %d bytes written, got %d", len("hostname\r\n"), n2)
	}
}

func TestSessionService_IdempotencyCollision(t *testing.T) {
	h := setupSessionServiceTest(t)
	defer h.server.Close()
	ctx := context.Background()

	openParams := app.SessionOpenParams{
		Target:         h.target,
		Caller:         h.agentCaller,
		Reason:         "initial open",
		IdempotencyKey: "idem-collision-1",
		Timeout:        30 * time.Second,
		Cols:           80,
		Rows:           24,
	}

	if _, _, err := h.svc.OpenSession(ctx, openParams); err != nil {
		t.Fatalf("initial open failed: %v", err)
	}

	// Parameter collision (different reason) returns conflict
	openParamsConflict := openParams
	openParamsConflict.Reason = "different reason collision"
	if _, _, err := h.svc.OpenSession(ctx, openParamsConflict); err == nil {
		t.Errorf("expected collision error on modified parameters with same key")
	}
}

func TestSessionService_DestructiveRequiresApproval(t *testing.T) {
	h := setupSessionServiceTest(t)
	defer h.server.Close()
	ctx := context.Background()

	// Set safety resolver to destructive
	h.safetyResolver.resolution = app.SafetyResolution{
		Classification: domain.ClassDestructivePrivileged,
		Contained:      false,
	}

	openParams := app.SessionOpenParams{
		Target:         h.target,
		Caller:         h.agentCaller,
		Reason:         "open destructive session",
		IdempotencyKey: "idem-dest-1",
		Timeout:        30 * time.Second,
		Cols:           80,
		Rows:           24,
	}

	// Without approval -> denied
	_, _, err := h.svc.OpenSession(ctx, openParams)
	if err == nil {
		t.Fatal("expected denial error on destructive operation without approval")
	}
	var deniedErr *app.PolicyDeniedError
	if !errors.As(err, &deniedErr) {
		t.Errorf("expected PolicyDeniedError, got: %v", err)
	}

	openParamsApproved := openParams
	openParamsApproved.IdempotencyKey = "idem-dest-approved"

	fixedTime := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	// Calculate fingerprint for the destructive operation
	openOp := domain.Operation{
		Kind:                "session.open",
		Target:              domain.MachineRef(h.target),
		Actor:               h.agentCaller,
		Reason:              openParamsApproved.Reason,
		Deadline:            fixedTime.Add(openParamsApproved.Timeout),
		IdempotencyKey:      openParamsApproved.IdempotencyKey,
		RequiredCapability:  domain.CapabilitySessionOpen,
		RequiredScopes:      []string{domain.ScopeSessionOpen},
		Classification:      domain.ClassDestructivePrivileged,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters: map[string]any{
			"cols": uint16(80),
			"rows": uint16(24),
			"term": "xterm-256color",
		},
	}
	opFp, _ := openOp.Fingerprint()

	// Create valid approval
	appObj := &domain.Approval{
		ID:              "app-0123456789abcdef0123456789abcdef",
		Actor:           domain.ActorID(h.agentCaller.EffectiveActor),
		Target:          domain.MachineRef(h.target),
		AuthorizedClass: domain.ClassDestructivePrivileged,
		Fingerprint:     opFp,
		IdempotencyKey:  openParamsApproved.IdempotencyKey,
		IssuedAt:        fixedTime.Add(-time.Minute),
		ExpiresAt:       fixedTime.Add(time.Hour),
	}

	openParamsApproved.Approval = appObj
	if err := h.approvalStore.Issue(*appObj); err != nil {
		t.Fatal(err)
	}
	obs, rcpt, err := h.svc.OpenSession(ctx, openParamsApproved)
	if err != nil {
		t.Fatalf("OpenSession with approval failed: %v", err)
	}
	if obs == nil || rcpt == nil || rcpt.Outcome.Status != domain.OutcomeSuccess {
		t.Errorf("unexpected success outcome: obs=%+v, rcpt=%+v", obs, rcpt)
	}

	// Reusing consumed approval on another operation fails
	openOp2 := openOp
	openOp2.IdempotencyKey = "idem-dest-2"
	opFp2, _ := openOp2.Fingerprint()

	appObjReused := &domain.Approval{
		ID:              appObj.ID, // Same approval ID already marked consumed
		Actor:           domain.ActorID(h.agentCaller.EffectiveActor),
		Target:          domain.MachineRef(h.target),
		AuthorizedClass: domain.ClassDestructivePrivileged,
		Fingerprint:     opFp2,
		IdempotencyKey:  "idem-dest-2",
		IssuedAt:        fixedTime.Add(-time.Minute),
		ExpiresAt:       fixedTime.Add(time.Hour),
	}

	openParams2 := openParamsApproved
	openParams2.IdempotencyKey = "idem-dest-2"
	openParams2.Approval = appObjReused
	_, _, err = h.svc.OpenSession(ctx, openParams2)
	if err == nil || !errors.Is(err, approval.ErrApprovalNotIssued) {
		t.Errorf("expected copied approval to fail provenance validation, got: %v", err)
	}
}
