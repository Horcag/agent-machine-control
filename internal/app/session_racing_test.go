package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestSessionService_InFlightConcurrentRacingDeduplication(t *testing.T) {
	h := setupSessionServiceTest(t)
	defer h.server.Close()
	ctx := context.Background()

	obs, _, err := h.svc.OpenSession(ctx, app.SessionOpenParams{
		Target:         h.target,
		Caller:         h.agentCaller,
		Reason:         "open session for racing test",
		IdempotencyKey: "idem-racing-open",
		Timeout:        30 * time.Second,
	})
	if err != nil {
		t.Fatalf("OpenSession failed: %v", err)
	}

	const goroutines = 5
	errs := make([]error, goroutines)
	receiptIDs := make([]domain.ReceiptID, goroutines)
	bytesWritten := make([]int, goroutines)

	writeParams := app.SessionWriteParams{
		SessionID:      obs.ID,
		Caller:         h.agentCaller,
		Data:           "single write\r\n",
		Reason:         "racing retry",
		IdempotencyKey: "idem-racing-write",
		Timeout:        30 * time.Second,
	}

	syncChan := make(chan struct{})
	finishedChan := make(chan int, goroutines)

	for i := range goroutines {
		go func(idx int) {
			<-syncChan
			n, rcpt, runErr := h.svc.WriteSession(ctx, writeParams)
			errs[idx] = runErr
			bytesWritten[idx] = n
			if rcpt != nil {
				receiptIDs[idx] = rcpt.ReceiptID
			}
			finishedChan <- idx
		}(i)
	}

	close(syncChan)

	for range goroutines {
		<-finishedChan
	}

	for i := range goroutines {
		if errs[i] != nil {
			t.Errorf("goroutine %d failed: %v", i, errs[i])
		}
		if receiptIDs[i] == "" {
			t.Errorf("goroutine %d missing receipt ID", i)
		}
		if receiptIDs[i] != receiptIDs[0] {
			t.Errorf("goroutine %d received different receipt ID: %s vs %s", i, receiptIDs[i], receiptIDs[0])
		}
		if bytesWritten[i] != bytesWritten[0] {
			t.Errorf("goroutine %d reported different bytes written: %d vs %d", i, bytesWritten[i], bytesWritten[0])
		}
	}
}

func TestSessionService_CrossActorAndScopeDenials(t *testing.T) {
	h := setupSessionServiceTest(t)
	defer h.server.Close()
	ctx := context.Background()

	obs, _, err := h.svc.OpenSession(ctx, app.SessionOpenParams{
		Target:         h.target,
		Caller:         h.agentCaller,
		Reason:         "open test session",
		IdempotencyKey: "idem-actor-1",
		Timeout:        30 * time.Second,
	})
	if err != nil {
		t.Fatalf("OpenSession failed: %v", err)
	}

	otherPerms := domain.NewScopeSet(
		domain.ScopeSessionRead,
		domain.ScopeSessionWrite,
		"evidence:sensitive",
	)
	otherCaller := domain.ActorContext{
		AuthenticatedCaller:  "other-agent",
		EffectiveActor:       "other-agent",
		CallerPermissions:    otherPerms,
		EffectivePermissions: otherPerms,
	}

	// Unauthorized agent receives ErrSessionNotFound (no existence leak)
	_, err = h.svc.GetSession(ctx, obs.ID, otherCaller)
	if err != domain.ErrSessionNotFound {
		t.Errorf("expected ErrSessionNotFound for non-owner, got: %v", err)
	}

	// Owner without sensitive evidence scope receives access denied
	noEvidencePerms := domain.NewScopeSet(
		domain.ScopeSessionRead,
		domain.ScopeSessionWrite,
	)
	noEvidenceCaller := domain.ActorContext{
		AuthenticatedCaller:  "agent-builder",
		EffectiveActor:       "agent-builder",
		CallerPermissions:    noEvidencePerms,
		EffectivePermissions: noEvidencePerms,
	}

	_, _, _, _, _, err = h.svc.ReadSession(ctx, obs.ID, noEvidenceCaller, 0, 1024)
	if err != domain.ErrSessionAccessDenied {
		t.Errorf("expected ErrSessionAccessDenied without sensitive evidence scope, got: %v", err)
	}

	_, _, _, _, _, err = h.svc.WaitSession(ctx, obs.ID, noEvidenceCaller, 10*time.Millisecond, "", 0, 100*time.Millisecond)
	if err != domain.ErrSessionAccessDenied {
		t.Errorf("expected ErrSessionAccessDenied for WaitSession without sensitive evidence scope, got: %v", err)
	}

	noReadPerms := domain.NewScopeSet(domain.ScopeSessionWrite)
	noReadCaller := domain.ActorContext{
		AuthenticatedCaller:  "agent-builder",
		EffectiveActor:       "agent-builder",
		CallerPermissions:    noReadPerms,
		EffectivePermissions: noReadPerms,
	}

	if _, err := h.svc.ListSessions(ctx, noReadCaller, domain.MachineRef(h.target)); err != domain.ErrSessionAccessDenied {
		t.Errorf("expected ErrSessionAccessDenied on ListSessions without read scope, got: %v", err)
	}
	if _, err := h.svc.GetSession(ctx, obs.ID, noReadCaller); err != domain.ErrSessionAccessDenied {
		t.Errorf("expected ErrSessionAccessDenied on GetSession without read scope, got: %v", err)
	}

	list, err := h.svc.ListSessions(ctx, h.agentCaller, domain.MachineRef(h.target))
	if err != nil || len(list) != 1 {
		t.Errorf("expected 1 session from ListSessions, got %v (err: %v)", list, err)
	}
}

func TestSessionService_ParameterValidationErrors(t *testing.T) {
	h := setupSessionServiceTest(t)
	defer h.server.Close()
	ctx := context.Background()

	badID := domain.SessionID("sess-00000000000000000000000000000000")

	// Invalid Open target (empty)
	_, _, err := h.svc.OpenSession(ctx, app.SessionOpenParams{
		Target:         "",
		Caller:         h.agentCaller,
		Reason:         "valid reason",
		IdempotencyKey: "k",
		Timeout:        30 * time.Second,
	})
	if err == nil {
		t.Errorf("expected error on empty target")
	}

	// Write to non-existent session
	_, _, err = h.svc.WriteSession(ctx, app.SessionWriteParams{
		SessionID:      badID,
		Caller:         h.agentCaller,
		Data:           "dir\r\n",
		Reason:         "valid reason",
		IdempotencyKey: "k-bad-write",
		Timeout:        30 * time.Second,
	})
	if err == nil || !errors.Is(err, domain.ErrSessionNotFound) {
		t.Errorf("expected ErrSessionNotFound on bad Write, got: %v", err)
	}

	// Control non-existent session
	_, err = h.svc.ControlSession(ctx, app.SessionControlParams{
		SessionID:      badID,
		Caller:         h.agentCaller,
		Key:            domain.ControlKeyCtrlC,
		Reason:         "valid reason",
		IdempotencyKey: "k-bad-ctrl",
		Timeout:        30 * time.Second,
	})
	if err == nil || !errors.Is(err, domain.ErrSessionNotFound) {
		t.Errorf("expected ErrSessionNotFound on bad Control, got: %v", err)
	}

	// Close non-existent session
	_, _, err = h.svc.CloseSession(ctx, app.SessionCloseParams{
		SessionID:      badID,
		Caller:         h.agentCaller,
		Reason:         "valid reason",
		IdempotencyKey: "k-bad-close",
		Timeout:        30 * time.Second,
	})
	if err == nil || !errors.Is(err, domain.ErrSessionNotFound) {
		t.Errorf("expected ErrSessionNotFound on bad Close, got: %v", err)
	}
}

func TestSessionService_PolicyDenialReceipt(t *testing.T) {
	h := setupSessionServiceTest(t)
	defer h.server.Close()
	ctx := context.Background()

	// Caller missing ScopeSessionOpen
	noOpenPerms := domain.NewScopeSet(domain.ScopeSessionRead, domain.ScopeSessionWrite)
	noOpenCaller := domain.ActorContext{
		AuthenticatedCaller:  "agent-no-open",
		EffectiveActor:       "agent-no-open",
		CallerPermissions:    noOpenPerms,
		EffectivePermissions: noOpenPerms,
	}

	_, rcpt, err := h.svc.OpenSession(ctx, app.SessionOpenParams{
		Target:         h.target,
		Caller:         noOpenCaller,
		Reason:         "open without permission",
		IdempotencyKey: "idem-denied-open",
		Timeout:        30 * time.Second,
	})
	if err == nil {
		t.Fatalf("expected error on open without permission")
	}
	var deniedErr *app.PolicyDeniedError
	if !errors.As(err, &deniedErr) {
		t.Fatalf("expected PolicyDeniedError, got: %v", err)
	}
	if rcpt == nil || rcpt.Outcome.Status != domain.OutcomeDenied {
		t.Errorf("expected denied receipt, got: %+v", rcpt)
	}
}
