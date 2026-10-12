package app

import (
	"context"
	"reflect"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

type guestAdmissionKey struct{}

// The coordinator installs this only after resolving safety under the lease.
func withGuestAdmission(ctx context.Context, admitted *admittedSessionMutation) context.Context {
	if !domain.IsGuestOperation(admitted.op.Kind) {
		return ctx
	}
	return context.WithValue(ctx, guestAdmissionKey{}, admitted)
}

func (s *GuestService) validateEffectAuthority(ctx context.Context, p GuestOperationParams, config GuestConfiguration) error {
	admitted, ok := ctx.Value(guestAdmissionKey{}).(*admittedSessionMutation)
	if !ok || admitted.op.Target != domain.MachineRef(p.Target) || admitted.op.Actor.AuthenticatedCaller != p.Caller.AuthenticatedCaller || admitted.op.Actor.EffectiveActor != p.Caller.EffectiveActor {
		return ErrGuestAuthority
	}
	resolution, fresh, err := s.resolve(ctx, p)
	if err != nil || resolution.Locator.String() != p.Target || !reflect.DeepEqual(fresh, config) {
		return ErrGuestAuthority
	}
	current := s.session.resolveSafety(ctx, admitted.op.Target)
	if !reflect.DeepEqual(current, admitted.safety) || current.Classification != admitted.op.Classification {
		return ErrGuestAuthority
	}
	return ctx.Err()
}
