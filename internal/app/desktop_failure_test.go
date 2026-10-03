package app_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestDesktopFailedReceiptRetriesAndAdmission(t *testing.T) {
	for _, authority := range []string{"normal", "lab"} {
		for _, cause := range []error{errors.New(desktopSecret), context.DeadlineExceeded, context.Canceled, domain.ErrClipboardUncertain} {
			t.Run(authority+"/"+cause.Error(), func(t *testing.T) {
				testDesktopFailureRetry(t, authority, cause)
			})
		}
	}
}

func testDesktopFailureRetry(t *testing.T, authority string, cause error) {
	t.Helper()
	f := newConsoleFixture(t)
	req := desktopRequest(f, "clipboard.set")
	req.Request.Text = desktopSecret
	actor := f.actor
	if authority == "normal" {
		req = approveDesktop(t, f, req)
	} else {
		configureLab(f)
		req.LabGrantID = issueLab(t, f).GrantID
		actor = labActor(t)
	}
	p := desktopProvider(req)
	p.err = cause
	service := app.NewDesktopService(p, f.service)
	first, err := service.Action(t.Context(), actor, req)
	if err == nil || first.Receipt == nil || first.CachedReceipt {
		t.Fatalf("first failure: %+v %v", first, err)
	}
	retry, err := service.Action(t.Context(), actor, req)
	if err == nil || retry.Receipt == nil || !retry.CachedReceipt || !reflect.DeepEqual(first.Receipt, retry.Receipt) || len(p.targets) != 1 {
		t.Fatalf("retry: %+v %v dispatches=%d", retry, err, len(p.targets))
	}
	if !reflect.DeepEqual(retry.Response, domain.DesktopResponse{}) {
		t.Fatal("cached partial guest response escaped")
	}
	assertDesktopRedacted(t, f.root, retry, err)
	assertDesktopRejectedFailureRetries(t, req, actor, f, p, service)
}

func assertDesktopRejectedFailureRetries(t *testing.T, req app.DesktopActionRequest, actor domain.ActorContext, f consoleFixture, p *desktopProviderFake, service *app.DesktopService) {
	t.Helper()
	for _, variant := range []string{"actor", "target", "payload", "reason", "authority", "expired"} {
		changed, caller := req, actor
		switch variant {
		case "actor":
			caller = f.actor
			caller.EffectiveActor = "operator:other"
			caller.AuthenticatedCaller = "operator:other"
		case "target":
			changed.Target = "bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb"
		case "payload":
			changed.Request.Text = "other synthetic text"
		case "reason":
			changed.Reason = "different synthetic reason"
		case "authority":
			caller, _ = domain.NewActorContext(actor.AuthenticatedCaller, actor.EffectiveActor, domain.NewScopeSet(domain.ScopeMachineRead), domain.NewScopeSet(domain.ScopeMachineRead))
		case "expired":
			changed.Request.Deadline = f.now.Format("2006-01-02T15:04:05.999999999Z07:00")
		}
		out, err := service.Action(t.Context(), caller, changed)
		if err == nil || out.Receipt != nil || out.CachedReceipt || len(p.targets) != 1 {
			t.Fatalf("%s reused foreign receipt: %+v %v", variant, out, err)
		}
	}
}

func TestDesktopNonadmittedErrorsHaveNoReceipt(t *testing.T) {
	for _, variant := range []string{"invalid actor", "missing read", "missing evidence", "missing write", "invalid envelope", "expired", "unbounded", "foreign target", "derived approval", "mixed authority", "missing approval"} {
		t.Run(variant, func(t *testing.T) {
			f := newConsoleFixture(t)
			actor, req, _ := desktopAdmissionVariant(t, f, variant)
			p := desktopProvider(req)
			out, err := app.NewDesktopService(p, f.service).Action(t.Context(), actor, req)
			if err == nil || out.Receipt != nil || out.CachedReceipt || len(p.targets) != 0 {
				t.Fatalf("nonadmitted: %+v %v", out, err)
			}
		})
	}
}

func TestDesktopPreAdmissionCancellationDoesNotCreateCachedActionEvidence(t *testing.T) {
	f := newConsoleFixture(t)
	req := approveDesktop(t, f, desktopRequest(f, "clipboard.set"))
	p := desktopProvider(req)
	service := app.NewDesktopService(p, f.service)
	for range 2 {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		out, err := service.Action(ctx, f.actor, req)
		if err == nil || out.Receipt != nil || out.CachedReceipt || len(p.targets) != 0 {
			t.Fatalf("preadmission evidence: %+v %v", out, err)
		}
	}
	// A fresh context still executes the unconsumed authorization, not an abort cache.
	out, err := service.Action(t.Context(), f.actor, req)
	if err != nil || out.Receipt == nil || out.CachedReceipt || len(p.targets) != 1 {
		t.Fatalf("fresh admission: %+v %v", out, err)
	}
}
