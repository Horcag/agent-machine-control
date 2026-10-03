package app_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestClipboardSnapshotRefusesInvalidAuthority(t *testing.T) {
	for _, mode := range []string{"missing read", "missing evidence", "provider unavailable", "foreign target"} {
		t.Run(mode, func(t *testing.T) {
			f := newConsoleFixture(t)
			req := desktopRequest(f, "clipboard.snapshot")
			// Observation requires no write scope or mutation approval.
			scopes := domain.NewScopeSet(domain.ScopeMachineRead, domain.ScopeEvidenceCapture)
			p := desktopProvider(req)
			p.response.Text = ""
			p.response.Clipboard = &domain.DesktopClipboard{Sequence: 0, Formats: []uint32{}, InventoryComplete: true, Empty: true}
			switch mode {
			case "missing read":
				scopes = domain.NewScopeSet(domain.ScopeEvidenceCapture)
			case "missing evidence":
				scopes = domain.NewScopeSet(domain.ScopeMachineRead)
			case "provider unavailable":
				p.err = errors.New(desktopSecret)
			case "foreign target":
				req.Target = "bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb"
			}
			actor, err := domain.NewActorContext("agent:test", "agent:test", scopes, scopes)
			if err != nil {
				t.Fatal(err)
			}
			result, err := app.NewDesktopService(p, f.service).Action(context.Background(), actor, req)

			if err == nil || result.Response.Clipboard != nil {
				t.Fatal("failed observation returned data", result, err)
			}
			wantCalls := 0
			if mode == "provider unavailable" {
				wantCalls = 1
			}
			if len(p.targets) != wantCalls {
				t.Fatal("unexpected dispatch", p.targets)
			}
			assertDesktopRedacted(t, f.root, result, err)
		})
	}
}

func TestClipboardSnapshotUsesSensitiveObservationAuthority(t *testing.T) {
	f := newConsoleFixture(t)
	req := desktopRequest(f, "clipboard.snapshot")
	scopes := domain.NewScopeSet(domain.ScopeMachineRead, domain.ScopeEvidenceCapture)
	actor, err := domain.NewActorContext("agent:test", "agent:test", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	p := desktopProvider(req)
	p.response.Text = ""
	p.response.Clipboard = &domain.DesktopClipboard{Sequence: 0, Formats: []uint32{}, InventoryComplete: true, Empty: true}
	result, err := app.NewDesktopService(p, f.service).Action(context.Background(), actor, req)
	if err != nil || !reflect.DeepEqual(result.Response, p.response) || result.Receipt != nil || result.CachedReceipt || len(p.targets) != 1 || p.targets[0] != domain.MachineRef(desktopVMID) || !reflect.DeepEqual(p.requests[0], req.Request) {
		t.Fatal("snapshot observation lost authority or metadata", result, err, p.targets)
	}
	assertDesktopRedacted(t, f.root, result, err)
}
