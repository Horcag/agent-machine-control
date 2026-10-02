package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

type labChangingSafety struct {
	labSafetyFake
	calls int
	after func(int)
}

func (s *labChangingSafety) ResolveSafety(ctx context.Context, id domain.MachineRef) (app.SafetyResolution, error) {
	s.calls++
	result, err := s.labSafetyFake.ResolveSafety(ctx, id)
	if s.after != nil {
		s.after(s.calls)
	}
	return result, err
}

func TestConsoleLabAdmissionBoundsFreshTargetObservations(t *testing.T) {
	provider := &consoleProviderFake{}
	f := newBoundsRecordingFixture(t, provider)
	configureLab(f.consoleFixture)
	safety := &labChangingSafety{labSafetyFake: labSafetyFake{contained: true}}
	app.WithConsoleLabSafetyResolver(safety)(f.service)
	observations, err := f.backend.ListMachines(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	queries := 0
	f.backend.listMachinesFn = func(context.Context) ([]domain.MachineObservation, error) {
		queries++
		return observations, nil
	}
	grant := issueLab(t, f.consoleFixture)
	if queries != 1 || safety.calls != 1 {
		t.Fatalf("issuance queries=%d safety checks=%d; want one validated observation and rollback check", queries, safety.calls)
	}
	queries, safety.calls = 0, 0
	req := labRequest(grant)
	first, err := f.service.Input(t.Context(), labActor(t), req)
	if err != nil || len(provider.inputs) != 1 {
		t.Fatalf("first dispatch: %v, inputs=%d", err, len(provider.inputs))
	}
	// One action resolution, one admission binding, and one fresh fenced binding.
	if queries != 3 || safety.calls != 2 {
		t.Fatalf("mutation queries=%d safety checks=%d; want 3 and 2", queries, safety.calls)
	}
	queries, safety.calls = 0, 0
	second, err := f.service.Input(t.Context(), labActor(t), req)
	if err != nil || first.ReceiptID != second.ReceiptID || len(provider.inputs) != 1 || queries != 2 || safety.calls != 1 {
		t.Fatalf("retry err=%v inputs=%d queries=%d safety checks=%d", err, len(provider.inputs), queries, safety.calls)
	}
}

func TestConsoleLabFinalBindingRejectsChangedEnrollmentAndRollback(t *testing.T) {
	for _, changed := range []string{"enrollment", "rollback"} {
		t.Run(changed, func(t *testing.T) {
			testLabFinalBindingChange(t, changed)
		})
	}
}

func testLabFinalBindingChange(t *testing.T, changed string) {
	t.Helper()
	f := newConsoleFixture(t)
	configureLab(f)
	store, prior := configureProtectedLabEnrollment(t, f)
	grant := issueLab(t, f)
	safety := &labChangingSafety{labSafetyFake: labSafetyFake{contained: true}}
	safety.after = func(call int) {
		if call != 1 {
			return
		}
		if changed == "rollback" {
			safety.unverified = true
			return
		}
		if _, err := store.Clear(t.Context()); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Save(t.Context(), prior); err != nil {
			t.Fatal(err)
		}
	}
	app.WithConsoleLabSafetyResolver(safety)(f.service)
	_, err := f.service.Input(t.Context(), labActor(t), labRequest(grant))
	if !errors.Is(err, app.ErrInvalidConsoleLabGrant) || len(f.provider.inputs) != 0 {
		t.Fatalf("changed %s dispatched: err=%v inputs=%d", changed, err, len(f.provider.inputs))
	}
	wantChecks := 1
	if changed == "rollback" {
		wantChecks = 2
	}
	if safety.calls != wantChecks {
		t.Fatalf("changed %s safety checks=%d want=%d", changed, safety.calls, wantChecks)
	}
}
