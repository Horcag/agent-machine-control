package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestSessionServiceRejectsInvalidIDAtPublicBoundary(t *testing.T) {
	t.Parallel()

	h := setupSessionServiceTest(t)
	defer h.server.Close()
	ctx := context.Background()
	id := domain.SessionID("../outside")
	outside := domain.SessionObservation{
		ID:              "sess-0123456789abcdef0123456789abcdef",
		Target:          domain.MachineRef(h.target),
		OwnerActor:      h.agentCaller.EffectiveActor,
		State:           domain.SessionStateClosed,
		CreatedAt:       time.Now().UTC(),
		LastActivityAt:  time.Now().UTC(),
		ObservationType: domain.ObservationObserved,
	}
	data, err := json.Marshal(outside)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.stateDir, "outside.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	assertServiceInvalidSessionID(t, func() error { _, err := h.svc.GetSession(ctx, id, h.agentCaller); return err })
	assertServiceInvalidSessionID(t, func() error { _, _, _, _, _, err := h.svc.ReadSession(ctx, id, h.agentCaller, 0, 1024); return err })
	assertServiceInvalidSessionID(t, func() error {
		_, _, _, _, _, err := h.svc.WaitSession(ctx, id, h.agentCaller, time.Millisecond, "", 0, time.Millisecond)
		return err
	})
	assertServiceInvalidSessionID(t, func() error {
		_, _, err := h.svc.WriteSession(ctx, app.SessionWriteParams{SessionID: id, Caller: h.agentCaller})
		return err
	})
	assertServiceInvalidSessionID(t, func() error {
		_, err := h.svc.ControlSession(ctx, app.SessionControlParams{SessionID: id, Caller: h.agentCaller})
		return err
	})
	assertServiceInvalidSessionID(t, func() error {
		_, _, err := h.svc.CloseSession(ctx, app.SessionCloseParams{SessionID: id, Caller: h.agentCaller})
		return err
	})
}

func TestSessionServiceScopeDenialPrecedesReadIDValidation(t *testing.T) {
	t.Parallel()

	h := setupSessionServiceTest(t)
	defer h.server.Close()
	ctx := context.Background()
	id := domain.SessionID("../outside")
	noScopes := domain.NewScopeSet()
	caller, err := domain.NewActorContext("agent:no-scopes", "agent:no-scopes", noScopes, noScopes)
	if err != nil {
		t.Fatal(err)
	}

	for _, call := range []func() error{
		func() error { _, err := h.svc.GetSession(ctx, id, caller); return err },
		func() error { _, _, _, _, _, err := h.svc.ReadSession(ctx, id, caller, 0, 1024); return err },
		func() error {
			_, _, _, _, _, err := h.svc.WaitSession(ctx, id, caller, time.Millisecond, "", 0, time.Millisecond)
			return err
		},
	} {
		if err := call(); !errors.Is(err, domain.ErrSessionAccessDenied) {
			t.Fatalf("error = %v, want ErrSessionAccessDenied", err)
		}
	}
}

func assertServiceInvalidSessionID(t *testing.T, call func() error) {
	t.Helper()
	if err := call(); !errors.Is(err, domain.ErrInvalidSessionID) {
		t.Fatalf("error = %v, want ErrInvalidSessionID", err)
	}
}
