package app_test

import (
	"context"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

type labProviderSafety struct {
	labSafetyFake
	calls int
	t     *testing.T
}

func (s *labProviderSafety) ResolveSafety(ctx context.Context, providerID domain.MachineRef) (app.SafetyResolution, error) {
	s.t.Helper()
	if providerID != "c4a523d4-6b99-4d62-a5e2-4752c0f20001" {
		s.t.Fatalf("safety resolver received %q; want enrolled provider UUID", providerID)
	}
	s.calls++
	return s.labSafetyFake.ResolveSafety(ctx, providerID)
}

func TestConsoleLabSafetyUsesEnrolledProviderUUID(t *testing.T) {
	f := newConsoleFixture(t)
	configureLab(f)
	safety := &labProviderSafety{labSafetyFake: labSafetyFake{contained: true}, t: t}
	app.WithConsoleLabSafetyResolver(safety)(f.service)
	grant := issueLab(t, f)
	if grant.Target != "local:c4a523d4-6b99-4d62-a5e2-4752c0f20001" {
		t.Fatalf("grant canonical binding = %q", grant.Target)
	}
	if _, err := f.service.Input(context.Background(), labActor(t), labRequest(grant)); err != nil {
		t.Fatal(err)
	}
	if safety.calls < 2 || len(f.provider.inputs) != 1 {
		t.Fatalf("safety calls=%d, provider inputs=%d", safety.calls, len(f.provider.inputs))
	}
}
