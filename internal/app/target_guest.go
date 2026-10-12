package app

import (
	"context"
)

// ResolveGuestTarget resolves current protected enrollment and host authority.
// Guest operations authenticate the pinned SSH peer and original worker/root;
// they do not require a host VM state observation. Host recovery and interactive
// sessions continue to use ResolveTarget and its fresh provider observation.
func (s *TargetService) ResolveGuestTarget(ctx context.Context, reference string) (TargetResolution, error) {
	value, err := s.store.Load(ctx)
	if err != nil {
		return TargetResolution{}, err
	}
	if err = validateTargetReference(reference, value); err != nil {
		return TargetResolution{}, err
	}
	if _, err = s.targetHost(ctx, value.Locator); err != nil {
		return TargetResolution{}, err
	}
	resolution := TargetResolution{Locator: value.Locator, ProviderVMID: value.Locator.VMID}
	if err = resolution.Validate(); err != nil {
		return TargetResolution{}, err
	}
	return resolution, ctx.Err()
}
