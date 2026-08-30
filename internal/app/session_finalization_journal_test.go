package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/policy"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/sessions"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

type leaseCheckingSafetyResolver struct {
	leasePath string
	sawLease  bool
}

func (r *leaseCheckingSafetyResolver) ResolveSafety(context.Context, domain.MachineRef) (app.SafetyResolution, error) {
	if _, err := os.Stat(r.leasePath); err != nil {
		return app.SafetyResolution{Classification: domain.ClassDestructivePrivileged}, nil
	}
	r.sawLease = true
	return app.SafetyResolution{
		Classification: domain.ClassReversibleMutation,
		Contained:      true,
		RollbackRef:    "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
		RollbackState:  policy.RollbackState{Available: true, Verified: true, CheckpointID: "e4a523d4-6b99-4d62-a5e2-4752c0f20001"},
	}, nil
}

type finalizationHarness struct {
	sd        *statedir.StateDir
	manager   *sessions.Manager
	transport *trackingTransport
	safety    app.SafetyResolver
	actor     domain.ActorContext
	obs       *domain.SessionObservation
}

func newFinalizationHarness(t *testing.T) *finalizationHarness {
	t.Helper()
	sd, err := statedir.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	transport := &trackingTransport{}
	manager := sessions.NewManager(sd.SessionsDir(), transport, time.Now)
	safety := &mockSafetyResolver{resolution: app.SafetyResolution{
		Classification: domain.ClassReversibleMutation,
		Contained:      true,
		RollbackRef:    "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
		RollbackState: policy.RollbackState{
			Available: true, Verified: true, CheckpointID: "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
		},
	}}
	scopes := domain.NewScopeSet(domain.ScopeSessionOpen, domain.ScopeSessionRead, domain.ScopeSessionWrite)
	actor, err := domain.NewActorContext("agent:finalization", "agent:finalization", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	svc := app.NewSessionService(manager, safety, nil, audit.NewStore(sd.AuditDir()), receipt.NewStore(sd.ReceiptsDir()), approval.NewStore(sd.ApprovalsDir()))
	obs, _, err := svc.OpenSession(context.Background(), app.SessionOpenParams{
		Target:         "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Caller:         actor,
		Reason:         "open finalization test session",
		IdempotencyKey: "idem-finalization-open",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &finalizationHarness{sd: sd, manager: manager, transport: transport, safety: safety, actor: actor, obs: obs}
}

func (h *finalizationHarness) writeParams(key string) app.SessionWriteParams {
	return app.SessionWriteParams{
		SessionID: h.obs.ID, Caller: h.actor, Data: "synthetic write\r\n", Reason: "test durable finalization", IdempotencyKey: key,
	}
}

func (h *finalizationHarness) service(receipts *receipt.Store, audits *audit.Store, journal *sessions.MutationJournal) *app.SessionService {
	opts := []app.SessionOption{}
	if journal != nil {
		opts = append(opts, app.WithSessionMutationJournal(journal))
	}
	return app.NewSessionService(h.manager, h.safety, nil, audits, receipts, approval.NewStore(h.sd.ApprovalsDir()), opts...)
}

func assertPendingRetryHasNoEffect(t *testing.T, h *finalizationHarness, params app.SessionWriteParams) {
	t.Helper()
	before := atomic.LoadInt32(&h.transport.writeCalls)
	panicTransport := &trackingTransport{panicOnDial: true}
	restartedManager := sessions.NewManager(h.sd.SessionsDir(), panicTransport, time.Now)
	restarted := app.NewSessionService(restartedManager, h.safety, nil, audit.NewStore(h.sd.AuditDir()), receipt.NewStore(h.sd.ReceiptsDir()), approval.NewStore(h.sd.ApprovalsDir()))
	_, _, err := restarted.WriteSession(context.Background(), params)
	if !errors.Is(err, sessions.ErrMutationFinalizationPending) {
		t.Fatalf("retry error = %v, want pending finalization", err)
	}
	if atomic.LoadInt32(&h.transport.writeCalls) != before || atomic.LoadInt32(&panicTransport.writeCalls) != 0 {
		t.Fatal("pending retry performed a second transport effect")
	}
}

func TestSessionMutationJournal_PreventsReplayAfterReceiptAuditAndFinalizeFailures(t *testing.T) {
	tests := []struct {
		name  string
		build func(*finalizationHarness) (*receipt.Store, *audit.Store, *sessions.MutationJournal)
	}{
		{
			name: "receipt write",
			build: func(h *finalizationHarness) (*receipt.Store, *audit.Store, *sessions.MutationJournal) {
				return receipt.NewStore(h.sd.ReceiptsDir(), receipt.WithSaveHook(func(r domain.Receipt) error {
					if r.OperationKind == "session.write" {
						return errors.New("synthetic receipt failure")
					}
					return nil
				})), audit.NewStore(h.sd.AuditDir()), nil
			},
		},
		{
			name: "terminal audit write",
			build: func(h *finalizationHarness) (*receipt.Store, *audit.Store, *sessions.MutationJournal) {
				return receipt.NewStore(h.sd.ReceiptsDir()), audit.NewStore(h.sd.AuditDir(), audit.WithAppendHook(func(event audit.Event) error {
					if event.EventType == audit.EventTerminalOutcome {
						return errors.New("synthetic terminal audit failure")
					}
					return nil
				})), nil
			},
		},
		{
			name: "reservation finalize",
			build: func(h *finalizationHarness) (*receipt.Store, *audit.Store, *sessions.MutationJournal) {
				journal := sessions.NewMutationJournal(filepath.Join(h.sd.SessionsDir(), "mutations"), sessions.WithMutationJournalHook(func(action string) error {
					if action == "finalize" {
						return errors.New("synthetic reservation finalize failure")
					}
					return nil
				}))
				return receipt.NewStore(h.sd.ReceiptsDir()), audit.NewStore(h.sd.AuditDir()), journal
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newFinalizationHarness(t)
			receipts, audits, journal := tc.build(h)
			params := h.writeParams("idem-finalization-" + strings.ReplaceAll(tc.name, " ", "-"))
			_, rcpt, err := h.service(receipts, audits, journal).WriteSession(context.Background(), params)
			if err == nil || rcpt == nil || atomic.LoadInt32(&h.transport.writeCalls) != 1 {
				t.Fatalf("effect/finalization result: writes=%d receipt=%v err=%v", h.transport.writeCalls, rcpt, err)
			}
			assertPendingRetryHasNoEffect(t, h, params)
		})
	}
}

func TestSessionMutationJournal_ReserveAndCancelFailuresFailClosedBeforeEffect(t *testing.T) {
	h := newFinalizationHarness(t)
	params := h.writeParams("idem-reservation-write-failure")
	journal := sessions.NewMutationJournal(filepath.Join(h.sd.SessionsDir(), "mutations"), sessions.WithMutationJournalHook(func(action string) error {
		if action == "reserve" {
			return errors.New("synthetic reservation write failure")
		}
		return nil
	}))
	_, rcpt, err := h.service(receipt.NewStore(h.sd.ReceiptsDir()), audit.NewStore(h.sd.AuditDir()), journal).WriteSession(context.Background(), params)
	if err == nil || rcpt != nil || atomic.LoadInt32(&h.transport.writeCalls) != 0 {
		t.Fatalf("reservation failure was not pre-effect: writes=%d receipt=%v err=%v", h.transport.writeCalls, rcpt, err)
	}

	params = h.writeParams("idem-reservation-cancel-failure")
	journal = sessions.NewMutationJournal(filepath.Join(h.sd.SessionsDir(), "mutations"), sessions.WithMutationJournalHook(func(action string) error {
		if action == "cancel" {
			return errors.New("synthetic reservation cancel failure")
		}
		return nil
	}))
	audits := audit.NewStore(h.sd.AuditDir(), audit.WithAppendHook(func(event audit.Event) error {
		if event.EventType == audit.EventAdmissionIntent {
			return errors.New("synthetic admission audit failure")
		}
		return nil
	}))
	_, _, err = h.service(receipt.NewStore(h.sd.ReceiptsDir()), audits, journal).WriteSession(context.Background(), params)
	if err == nil || atomic.LoadInt32(&h.transport.writeCalls) != 0 {
		t.Fatalf("cancel failure allowed effect: writes=%d err=%v", h.transport.writeCalls, err)
	}
	assertPendingRetryHasNoEffect(t, h, params)
}

func TestSessionMutationJournal_OpenAndCloseRetriesUseImmutableResultsWithoutReadScope(t *testing.T) {
	sd, err := statedir.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	transport := &trackingTransport{}
	manager := sessions.NewManager(sd.SessionsDir(), transport, time.Now)
	safety := &mockSafetyResolver{resolution: app.SafetyResolution{
		Classification: domain.ClassReversibleMutation,
		Contained:      true,
		RollbackRef:    "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
		RollbackState:  policy.RollbackState{Available: true, Verified: true, CheckpointID: "e4a523d4-6b99-4d62-a5e2-4752c0f20001"},
	}}
	svc := app.NewSessionService(manager, safety, nil, audit.NewStore(sd.AuditDir()), receipt.NewStore(sd.ReceiptsDir()), approval.NewStore(sd.ApprovalsDir()))
	scopes := domain.NewScopeSet(domain.ScopeSessionOpen, domain.ScopeSessionClose)
	actor, err := domain.NewActorContext("agent:no-read", "agent:no-read", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	openParams := app.SessionOpenParams{
		Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Caller: actor, Reason: "open without read scope", IdempotencyKey: "idem-no-read-open",
	}
	opened, openReceipt, err := svc.OpenSession(context.Background(), openParams)
	if err != nil {
		t.Fatal(err)
	}
	closeParams := app.SessionCloseParams{
		SessionID: opened.ID, Caller: actor, Reason: "close without read scope", IdempotencyKey: "idem-no-read-close",
	}
	closed, closeReceipt, err := svc.CloseSession(context.Background(), closeParams)
	if err != nil || closed == nil || closed.State != domain.SessionStateClosed {
		t.Fatalf("close failed: observation=%v err=%v", closed, err)
	}
	closedRetry, closeRetryReceipt, err := svc.CloseSession(context.Background(), closeParams)
	if err != nil || closeRetryReceipt.ReceiptID != closeReceipt.ReceiptID || closedRetry.State != domain.SessionStateClosed {
		t.Fatalf("close retry did not use immutable result: observation=%v err=%v", closedRetry, err)
	}
	openedRetry, openRetryReceipt, err := svc.OpenSession(context.Background(), openParams)
	if err != nil || openRetryReceipt.ReceiptID != openReceipt.ReceiptID || openedRetry.State != domain.SessionStateActive {
		t.Fatalf("open retry returned mutable later state: observation=%v err=%v", openedRetry, err)
	}
}

func rewriteTerminalAuditFingerprint(t *testing.T, path, receiptID string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	matched := false
	for i, line := range lines {
		var event audit.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.EventType == audit.EventTerminalOutcome && event.ReceiptID == receiptID {
			event.Fingerprint = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
			encoded, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			lines[i] = string(encoded)
			matched = true
		}
	}
	if !matched {
		t.Fatal("terminal audit event not found for mismatch fixture")
	}
	// #nosec G703 -- path is the task-owned temporary audit fixture created by this test.
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

type terminalAuditReplayCase struct {
	name       string
	mutate     func(*testing.T, string, string)
	wantReplay bool
}

func removeTerminalAudit(t *testing.T, path, _ string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func truncateTerminalAudit(t *testing.T, path, _ string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G703 -- path is the task-owned temporary audit fixture created by this test.
	if err := os.WriteFile(path, data[:len(data)-10], 0600); err != nil {
		t.Fatal(err)
	}
}

func corruptTerminalAudit(t *testing.T, path, _ string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{corrupt audit record}\n"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertTerminalAuditReplay(t *testing.T, tc terminalAuditReplayCase, receiptValue, replayReceipt *domain.Receipt, replayErr error) {
	t.Helper()
	if tc.wantReplay {
		if replayErr != nil || replayReceipt == nil || replayReceipt.ReceiptID != receiptValue.ReceiptID {
			t.Fatalf("valid terminal evidence replay failed: receipt=%v err=%v", replayReceipt, replayErr)
		}
		return
	}
	if !errors.Is(replayErr, audit.ErrTerminalEvidenceInvalid) {
		t.Fatalf("invalid terminal evidence error = %v, want ErrTerminalEvidenceInvalid", replayErr)
	}
}

func runTerminalAuditReplayCase(t *testing.T, tc terminalAuditReplayCase) {
	t.Helper()
	h := newFinalizationHarness(t)
	params := h.writeParams("idem-audit-replay-" + tc.name)
	_, receiptValue, err := h.service(receipt.NewStore(h.sd.ReceiptsDir()), audit.NewStore(h.sd.AuditDir()), nil).WriteSession(context.Background(), params)
	if err != nil || receiptValue == nil {
		t.Fatalf("initial write failed: receipt=%v err=%v", receiptValue, err)
	}
	auditPath := filepath.Join(h.sd.AuditDir(), audit.AuditFileName)
	if tc.mutate != nil {
		tc.mutate(t, auditPath, string(receiptValue.ReceiptID))
	}

	panicTransport := &trackingTransport{panicOnDial: true}
	restartedManager := sessions.NewManager(h.sd.SessionsDir(), panicTransport, time.Now)
	restarted := app.NewSessionService(restartedManager, h.safety, nil, audit.NewStore(h.sd.AuditDir()), receipt.NewStore(h.sd.ReceiptsDir()), approval.NewStore(h.sd.ApprovalsDir()))
	before := atomic.LoadInt32(&h.transport.writeCalls)
	_, replayReceipt, replayErr := restarted.WriteSession(context.Background(), params)
	if atomic.LoadInt32(&h.transport.writeCalls) != before || atomic.LoadInt32(&panicTransport.writeCalls) != 0 {
		t.Fatal("finalized replay performed a second transport effect")
	}
	assertTerminalAuditReplay(t, tc, receiptValue, replayReceipt, replayErr)
}

func TestFinalizedSessionReplayVerifiesTerminalAuditEvidence(t *testing.T) {
	tests := []terminalAuditReplayCase{
		{name: "valid", wantReplay: true},
		{name: "missing", mutate: removeTerminalAudit},
		{name: "truncated", mutate: truncateTerminalAudit},
		{name: "corrupt", mutate: corruptTerminalAudit},
		{name: "mismatched", mutate: rewriteTerminalAuditFingerprint},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runTerminalAuditReplayCase(t, tc)
		})
	}
}

func TestSessionSafetyResolutionRunsUnderHostMutationLease(t *testing.T) {
	sd, err := statedir.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	target := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	transport := &trackingTransport{}
	manager := sessions.NewManager(sd.SessionsDir(), transport, time.Now)
	resolver := &leaseCheckingSafetyResolver{leasePath: filepath.Join(sd.LeasesDir(), target+".lease.json")}
	leaseManager := lease.NewManager(sd.LeasesDir())
	svc := app.NewSessionService(manager, resolver, leaseManager, audit.NewStore(sd.AuditDir()), receipt.NewStore(sd.ReceiptsDir()), approval.NewStore(sd.ApprovalsDir()))
	scopes := domain.NewScopeSet(domain.ScopeSessionOpen)
	actor, err := domain.NewActorContext("agent:lease-check", "agent:lease-check", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.OpenSession(context.Background(), app.SessionOpenParams{Target: target, Caller: actor, Reason: "verify lease ordering", IdempotencyKey: "idem-lease-before-safety"}); err != nil {
		t.Fatal(err)
	}
	if !resolver.sawLease {
		t.Fatal("safety resolver ran without the host-visible mutation lease")
	}
}
