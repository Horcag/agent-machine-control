package app

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/target"
)

// WithTargetObserver selects fresh exact inspection for enrolled-target operations.
// Discovery and authority planning continue to use the inventory refresh dependency.
func WithTargetObserver(observer MachineObserver) TargetOption {
	return func(service *TargetService) { service.observer = observer }
}

// ResolveTarget resolves only protected aliases or the exact enrolled identity.
func (s *TargetService) ResolveTarget(ctx context.Context, reference string) (TargetResolution, error) {
	resolution, _, err := s.ObserveTarget(ctx, reference)
	return resolution, err
}

// ObserveTarget returns the resolution and its single fresh validated observation.
// Services without an observer retain the inventory refresh boundary for tests and
// existing callers, and reuse its observation without a second provider query.
func (s *TargetService) ObserveTarget(ctx context.Context, reference string) (TargetResolution, domain.MachineObservation, error) {
	value, err := s.store.Load(ctx)
	if err != nil {
		return TargetResolution{}, domain.MachineObservation{}, err
	}
	if err := validateTargetReference(reference, value); err != nil {
		return TargetResolution{}, domain.MachineObservation{}, err
	}
	host, err := s.targetHost(ctx, value.Locator)
	if err != nil {
		return TargetResolution{}, domain.MachineObservation{}, err
	}
	queryCtx, cancel := context.WithTimeout(ctx, host.effectiveQueryTimeout())
	defer cancel()
	observation, err := s.observeCanonicalTarget(queryCtx, value.Locator)
	if queryErr := queryCtx.Err(); queryErr != nil {
		return TargetResolution{}, domain.MachineObservation{}, queryErr
	}
	if err != nil {
		return TargetResolution{}, domain.MachineObservation{}, err
	}
	if err := observation.Validate(); err != nil {
		return TargetResolution{}, domain.MachineObservation{}, err
	}
	observation.ID, _ = domain.NormalizeMachineGUID(observation.ID)
	if observation.ID != value.Locator.VMID || observation.HostID != domain.LocalHostID || observation.Locator != value.Locator {
		return TargetResolution{}, domain.MachineObservation{}, fmt.Errorf("%w: observation does not match enrolled target", domain.ErrInvalidMachineLocator)
	}
	resolution := TargetResolution{Locator: value.Locator, ProviderVMID: observation.ID, DisplayName: observation.Name}
	return resolution, observation.Clone(), nil
}

func validateTargetReference(reference string, value target.Default) error {
	if strings.TrimSpace(reference) != reference {
		return target.ErrDifferentTarget
	}
	if reference == "" || reference == "default" || slices.Contains(value.Aliases, reference) {
		return nil
	}
	if reference == value.Locator.String() {
		return nil
	}
	if guid, err := domain.NormalizeMachineGUID(reference); err == nil && guid == value.Locator.VMID {
		return nil
	}
	return target.ErrDifferentTarget
}

func (s *TargetService) targetHost(ctx context.Context, locator domain.MachineLocator) (HostEntry, error) {
	if err := ctx.Err(); err != nil {
		return HostEntry{}, err
	}
	if locator.HostID != domain.LocalHostID {
		return HostEntry{}, target.ErrUnsupportedHost
	}
	for _, host := range s.inventory.Hosts() {
		if host.ID == domain.LocalHostID && host.Enabled {
			return host, nil
		}
	}
	return HostEntry{}, domain.ErrMachineHostDisabled
}

func (s *TargetService) observeCanonicalTarget(ctx context.Context, locator domain.MachineLocator) (domain.MachineObservation, error) {
	if s.observer != nil {
		return s.observer.InspectMachine(ctx, locator.VMID)
	}
	if err := s.refreshInventory(ctx); err != nil {
		return domain.MachineObservation{}, err
	}
	entry, err := s.inventory.ResolveMachine(locator.String())
	return entry.Observation, err
}
