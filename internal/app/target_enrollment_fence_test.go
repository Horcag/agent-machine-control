package app

import (
	"context"
	"errors"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/target"
)

func TestTargetServiceEnrollmentIdentityRejectsAbsentAndForeignAuthority(t *testing.T) {
	ctx := context.Background()
	observation := targetObservation(t, domain.LocalHostID, targetVMA, "vm-alpha")
	inventory := targetInventory(t, nil, observation)
	store, _ := targetStore(t)
	service := targetService(t, inventory, store)
	if identity, err := service.EnrollmentIdentity(ctx, observation.Locator.String()); identity != "" || !errors.Is(err, target.ErrNoDefault) {
		t.Fatalf("absent enrollment identity = %q, %v", identity, err)
	}
	if _, _, err := service.EnrollDefaultTarget(ctx, observation.ID, nil); err != nil {
		t.Fatal(err)
	}
	foreign := targetObservation(t, domain.LocalHostID, targetVMB, "vm-bravo")
	if identity, err := service.EnrollmentIdentity(ctx, foreign.Locator.String()); identity != "" || !errors.Is(err, target.ErrDifferentTarget) {
		t.Fatalf("foreign enrollment identity = %q, %v", identity, err)
	}
	if publication, err := service.ClearDefaultTarget(ctx); err != nil || !publication.Durable {
		t.Fatalf("clear = %+v, %v", publication, err)
	}
	if identity, err := service.EnrollmentIdentity(ctx, observation.Locator.String()); identity != "" || !errors.Is(err, target.ErrNoDefault) {
		t.Fatalf("cleared enrollment identity = %q, %v", identity, err)
	}
}

func TestTargetServiceEnrollmentIdentitySurvivesRestartButChangesOnReEnrollment(t *testing.T) {
	ctx := context.Background()
	observation := targetObservation(t, domain.LocalHostID, targetVMA, "vm-alpha")
	inventory := targetInventory(t, nil, observation)
	store, dir := targetStore(t)
	service := targetService(t, inventory, store)
	if _, _, err := service.EnrollDefaultTarget(ctx, observation.ID, nil); err != nil {
		t.Fatal(err)
	}
	before, err := service.EnrollmentIdentity(ctx, observation.Locator.String())
	if err != nil || len(before) != 64 {
		t.Fatalf("initial identity = %q, %v", before, err)
	}
	reopenedStore, err := target.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	reopened := targetService(t, inventory, reopenedStore)
	stable, err := reopened.EnrollmentIdentity(ctx, observation.Locator.String())
	if err != nil || stable != before {
		t.Fatalf("restarted identity = %q, %v; want %q", stable, err, before)
	}
	if _, err := reopened.ClearDefaultTarget(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reopened.EnrollDefaultTarget(ctx, observation.ID, nil); err != nil {
		t.Fatal(err)
	}
	after, err := service.EnrollmentIdentity(ctx, observation.Locator.String())
	if err != nil || len(after) != 64 || after == before {
		t.Fatalf("new publication identity = %q, %v; prior %q", after, err, before)
	}
}
