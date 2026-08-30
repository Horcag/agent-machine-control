package app

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/target"
)

// TargetResolution separates canonical policy identity from the provider GUID boundary.
type TargetResolution struct {
	Locator      domain.MachineLocator
	ProviderVMID string
	DisplayName  string
}

// Validate checks that one local canonical identity and provider GUID agree.
func (r TargetResolution) Validate() error {
	if err := r.Locator.Validate(); err != nil {
		return err
	}
	if r.Locator.HostID != domain.LocalHostID {
		return target.ErrUnsupportedHost
	}
	providerID, err := domain.NormalizeMachineGUID(r.ProviderVMID)
	if err != nil || providerID != r.ProviderVMID || providerID != r.Locator.VMID {
		return fmt.Errorf("%w: provider VM GUID does not match canonical locator", domain.ErrInvalidMachineLocator)
	}
	return nil
}

// TargetService owns durable default enrollment and inventory-backed resolution.
type TargetService struct {
	inventory *TrustedInventory
	store     *target.Store
}

// NewTargetService constructs the shared non-transport target seam.
func NewTargetService(inventory *TrustedInventory, store *target.Store) (*TargetService, error) {
	if inventory == nil || store == nil {
		return nil, errors.New("app: target service requires inventory and store")
	}
	return &TargetService{inventory: inventory, store: store}, nil
}

// EnrollDefaultTarget resolves one observed local VM and atomically persists only its canonical identity.
func (s *TargetService) EnrollDefaultTarget(
	ctx context.Context,
	reference string,
	aliases []string,
) (TargetResolution, target.Publication, error) {
	if err := ctx.Err(); err != nil {
		return TargetResolution{}, target.Publication{}, err
	}
	entry, err := s.inventory.ResolveMachine(reference)
	if err != nil {
		return TargetResolution{}, target.Publication{}, err
	}
	if entry.Locator.HostID != domain.LocalHostID {
		return TargetResolution{}, target.Publication{}, target.ErrUnsupportedHost
	}
	value, err := target.NewDefault(entry.Locator, aliases)
	if err != nil {
		return TargetResolution{}, target.Publication{}, err
	}
	if err := s.inventory.ValidateMachineAliases(value.Locator, value.Aliases); err != nil {
		return TargetResolution{}, target.Publication{}, err
	}
	resolution, err := resolutionFromEntry(entry)
	if err != nil {
		return TargetResolution{}, target.Publication{}, err
	}
	publication, err := s.store.Save(ctx, value)
	return resolution, publication, err
}

// ResolveTarget resolves default, stored alias, or an exact inventory reference to the enrolled identity.
func (s *TargetService) ResolveTarget(ctx context.Context, reference string) (TargetResolution, error) {
	if err := ctx.Err(); err != nil {
		return TargetResolution{}, err
	}
	value, err := s.store.Load(ctx)
	if err != nil {
		return TargetResolution{}, err
	}

	locator := value.Locator
	if reference != "" && reference != "default" && !slices.Contains(value.Aliases, reference) {
		entry, err := s.inventory.ResolveMachine(reference)
		if err != nil {
			return TargetResolution{}, err
		}
		if entry.Locator != locator {
			return TargetResolution{}, target.ErrDifferentTarget
		}
	}
	return s.resolveCanonical(ctx, locator)
}

// ShowDefaultTarget returns the stored default only after inventory proves it remains routeable.
func (s *TargetService) ShowDefaultTarget(ctx context.Context) (TargetResolution, error) {
	return s.ResolveTarget(ctx, "default")
}

// ClearDefaultTarget removes the canonical target authority.
func (s *TargetService) ClearDefaultTarget(ctx context.Context) (target.Publication, error) {
	return s.store.Clear(ctx)
}

func (s *TargetService) resolveCanonical(ctx context.Context, locator domain.MachineLocator) (TargetResolution, error) {
	if err := ctx.Err(); err != nil {
		return TargetResolution{}, err
	}
	if locator.HostID != domain.LocalHostID {
		return TargetResolution{}, target.ErrUnsupportedHost
	}
	entry, err := s.inventory.ResolveMachine(locator.String())
	if err != nil {
		return TargetResolution{}, err
	}
	return resolutionFromEntry(entry)
}

func resolutionFromEntry(entry MachineIndexEntry) (TargetResolution, error) {
	resolution := TargetResolution{
		Locator:      entry.Locator,
		ProviderVMID: entry.Observation.ID,
		DisplayName:  entry.DisplayName,
	}
	if err := resolution.Validate(); err != nil {
		return TargetResolution{}, err
	}
	return resolution, nil
}
