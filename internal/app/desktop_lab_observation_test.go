package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestDesktopLabAdmissionBoundsFreshTargetObservations(t *testing.T) {
	f := newBoundsRecordingFixture(t, &consoleProviderFake{})
	f.backend.capabilitiesFn = func(context.Context, string) (domain.CapabilitySet, error) {
		return domain.NewCapabilitySet(domain.CapabilityConsoleInput, domain.CapabilityDesktopAction), nil
	}
	configureLab(f.consoleFixture)
	grant := issueLab(t, f.consoleFixture)
	safety := &labChangingSafety{labSafetyFake: labSafetyFake{contained: true}}
	app.WithConsoleLabSafetyResolver(safety)(f.service)
	observations, err := f.backend.ListMachines(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	queries := 0
	f.backend.listMachinesFn = func(context.Context) ([]domain.MachineObservation, error) { queries++; return observations, nil }
	req := desktopRequest(f.consoleFixture, "clipboard.set")
	req.LabGrantID = grant.GrantID
	p := desktopProvider(req)
	service := app.NewDesktopService(p, f.service)
	first, err := service.Action(t.Context(), labActor(t), req)
	if err != nil || first.Receipt == nil || first.CachedReceipt || len(p.targets) != 1 || queries != 2 || safety.calls != 2 {
		t.Fatalf("first admission: %+v err=%v dispatches=%d queries=%d safety=%d", first, err, len(p.targets), queries, safety.calls)
	}
	queries, safety.calls = 0, 0
	retry, err := service.Action(t.Context(), labActor(t), req)
	if err != nil || retry.Receipt == nil || retry.Receipt.ReceiptID != first.Receipt.ReceiptID || !retry.CachedReceipt || len(p.targets) != 1 || queries != 1 || safety.calls != 1 {
		t.Fatalf("retry: %+v err=%v dispatches=%d queries=%d safety=%d", retry, err, len(p.targets), queries, safety.calls)
	}
}

func TestDesktopLabFinalBindingRejectsChangedAuthorityWithoutActionEvidence(t *testing.T) {
	for _, changed := range []string{"enrollment", "rollback", "deadline", "revocation"} {
		t.Run(changed, func(t *testing.T) { testDesktopLabBindingChange(t, changed) })
	}
}

func testDesktopLabBindingChange(t *testing.T, changed string) {
	t.Helper()
	f := newConsoleFixture(t)
	configureLab(f)
	store, prior := configureProtectedLabEnrollment(t, f)
	grant := issueLab(t, f)
	req := desktopRequest(f, "clipboard.set")
	req.LabGrantID = grant.GrantID
	safety := &labChangingSafety{labSafetyFake: labSafetyFake{contained: true}}
	safety.after = func(call int) {
		if call != 1 {
			return
		}
		switch changed {
		case "rollback":
			safety.unverified = true
		case "deadline":
			*f.now = f.now.Add(time.Minute)
		case "enrollment":
			if _, err := store.Clear(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Save(t.Context(), prior); err != nil {
				t.Fatal(err)
			}
		}
	}
	if changed == "revocation" {
		_, err := f.service.RevokeLabGrant(t.Context(), f.actor, app.ConsoleLabGrantRevokeRequest{GrantID: grant.GrantID, Reason: "end synthetic desktop lab", IdempotencyKey: "desktop-lab-revoke", Deadline: req.Request.Deadline})
		if err != nil {
			t.Fatal(err)
		}
	}
	app.WithConsoleLabSafetyResolver(safety)(f.service)
	p := desktopProvider(req)
	service := app.NewDesktopService(p, f.service)
	for range 2 {
		out, err := service.Action(t.Context(), labActor(t), req)
		if err == nil || out.Receipt != nil || out.CachedReceipt || len(p.targets) != 0 {
			t.Fatalf("changed %s admission: %+v err=%v dispatches=%d", changed, out, err, len(p.targets))
		}
	}
	wantChecks := map[string]int{"enrollment": 1, "rollback": 3, "deadline": 1, "revocation": 0}[changed]
	if safety.calls != wantChecks {
		t.Fatalf("changed %s safety=%d want=%d", changed, safety.calls, wantChecks)
	}
}
