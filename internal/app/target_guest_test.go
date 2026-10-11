package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/target"
)

func TestGuestTargetResolvesProtectedIdentityWithoutHostObservation(t *testing.T) {
	original := targetObservation(t, domain.LocalHostID, targetVMA, "synthetic-name")
	observer := &exactTargetObserver{inspect: func(context.Context, string) (domain.MachineObservation, error) {
		t.Fatal("guest identity resolution must not query host VM state")
		return domain.MachineObservation{}, nil
	}}
	service, _ := exactTargetService(t, original, observer, func(context.Context) error {
		t.Fatal("guest identity resolution must not refresh fleet state")
		return nil
	})
	for _, ref := range []string{"", "default", "primary", targetVMA, strings.ToUpper(targetVMA), original.Locator.String()} {
		resolved, err := service.ResolveGuestTarget(t.Context(), ref)
		if err != nil || resolved.Validate() != nil || resolved.Locator != original.Locator || resolved.ProviderVMID != targetVMA || resolved.DisplayName != "" {
			t.Fatalf("reference %q: %+v, %v", ref, resolved, err)
		}
	}
	for _, ref := range []string{original.Name, targetVMB, "local:" + targetVMB, "remote:" + targetVMA, "PRIMARY", "default ", " " + targetVMA} {
		if resolved, err := service.ResolveGuestTarget(t.Context(), ref); !errors.Is(err, target.ErrDifferentTarget) || resolved != (TargetResolution{}) {
			t.Fatalf("foreign reference %q: %+v, %v", ref, resolved, err)
		}
	}
}

func TestGuestTargetRefreshesEnrollmentAndHostAuthority(t *testing.T) {
	original := targetObservation(t, domain.LocalHostID, targetVMA, "synthetic-name")
	service, inventory := exactTargetService(t, original, nil, func(context.Context) error {
		t.Fatal("guest resolution must not refresh inventory")
		return nil
	})
	if err := inventory.ReplaceHosts([]HostEntry{{ID: domain.LocalHostID, Address: "local", Enabled: false}}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveGuestTarget(t.Context(), "primary"); !errors.Is(err, domain.ErrMachineHostDisabled) {
		t.Fatalf("disabled host: %v", err)
	}
	if err := inventory.ReplaceHosts([]HostEntry{{ID: domain.LocalHostID, Address: "local", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.store.Clear(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveGuestTarget(t.Context(), "primary"); !errors.Is(err, target.ErrNoDefault) {
		t.Fatalf("cleared enrollment: %v", err)
	}
	locator, _ := domain.NewMachineLocator(domain.LocalHostID, targetVMB)
	value, _ := target.NewDefault(locator, []string{"replacement"})
	if _, err := service.store.Save(t.Context(), value); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveGuestTarget(t.Context(), targetVMA); !errors.Is(err, target.ErrDifferentTarget) {
		t.Fatalf("old enrollment: %v", err)
	}
	if resolved, err := service.ResolveGuestTarget(t.Context(), "replacement"); err != nil || resolved.ProviderVMID != targetVMB {
		t.Fatalf("new enrollment: %+v, %v", resolved, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := service.ResolveGuestTarget(ctx, "replacement"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled resolution: %v", err)
	}
}
