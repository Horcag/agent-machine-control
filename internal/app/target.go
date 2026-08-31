package app

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/target"
)

// TargetRefresh performs one caller-bounded read-only inventory refresh.
type TargetRefresh func(context.Context) error

// TargetOption configures TargetService dependencies.
type TargetOption func(*TargetService)

// WithTargetRefresh injects the fresh inventory boundary used before every public target interaction.
func WithTargetRefresh(refresh TargetRefresh) TargetOption {
	return func(service *TargetService) {
		service.refresh = refresh
	}
}

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
	refresh   TargetRefresh
}

// ListLocalCandidates returns fresh eligible local targets without changing authority state.
func (s *TargetService) ListLocalCandidates(ctx context.Context) ([]TargetResolution, error) {
	if err := s.refreshInventory(ctx); err != nil {
		return nil, err
	}
	entries, err := s.inventory.CurrentLocalMachines()
	if err != nil {
		return nil, err
	}
	resolutions := make([]TargetResolution, len(entries))
	for index, entry := range entries {
		resolution, err := resolutionFromEntry(entry)
		if err != nil {
			return nil, err
		}
		resolutions[index] = resolution
	}
	return resolutions, nil
}

// NewTargetService constructs the shared non-transport target seam.
func NewTargetService(inventory *TrustedInventory, store *target.Store, options ...TargetOption) (*TargetService, error) {
	if inventory == nil || store == nil {
		return nil, errors.New("app: target service requires inventory and store")
	}
	service := &TargetService{inventory: inventory, store: store, refresh: func(context.Context) error { return nil }}
	for _, option := range options {
		option(service)
	}
	if service.refresh == nil {
		return nil, errors.New("app: target service requires a refresh dependency")
	}
	return service, nil
}

// TargetPlan is an immutable logical target-authority transition prepared from fresh inventory.
type TargetPlan struct {
	Kind        domain.OperationKind
	Resolution  TargetResolution
	Prior       *target.Default
	Desired     *target.Default
	StateHash   string
	PriorHash   string
	DesiredHash string
	AliasCount  int
}

// PrepareEnrollDefaultTarget resolves and validates enrollment without changing durable authority.
func (s *TargetService) PrepareEnrollDefaultTarget(ctx context.Context, reference string, aliases []string) (TargetPlan, error) {
	if err := s.refreshInventory(ctx); err != nil {
		return TargetPlan{}, err
	}
	var entry MachineIndexEntry
	var err error
	if reference == "" {
		entry, err = s.inventory.ResolveSingleLocal()
	} else {
		entry, err = s.inventory.ResolveMachine(reference)
	}
	if err != nil {
		return TargetPlan{}, err
	}
	if entry.Locator.HostID != domain.LocalHostID {
		return TargetPlan{}, target.ErrUnsupportedHost
	}
	desired, err := target.NewDefault(entry.Locator, aliases)
	if err != nil {
		return TargetPlan{}, err
	}
	if err := s.inventory.ValidateMachineAliases(desired.Locator, desired.Aliases); err != nil {
		return TargetPlan{}, err
	}
	resolution, err := resolutionFromEntry(entry)
	if err != nil {
		return TargetPlan{}, err
	}
	prior, err := s.loadOptional(ctx)
	if err != nil {
		return TargetPlan{}, err
	}
	return newTargetPlan("target.enroll", resolution, prior, &desired), nil
}

// PrepareClearDefaultTarget validates a clear transition without changing durable authority.
func (s *TargetService) PrepareClearDefaultTarget(ctx context.Context) (TargetPlan, error) {
	if err := s.refreshInventory(ctx); err != nil {
		return TargetPlan{}, err
	}
	prior, err := s.store.Load(ctx)
	if err != nil {
		return TargetPlan{}, err
	}
	resolution, err := s.resolveCanonical(ctx, prior.Locator)
	if err != nil {
		return TargetPlan{}, err
	}
	return newTargetPlan("target.clear", resolution, &prior, nil), nil
}

func (s *TargetService) prepareReservedClear(ctx context.Context, locator domain.MachineLocator) (TargetPlan, error) {
	if err := s.refreshInventory(ctx); err != nil {
		return TargetPlan{}, err
	}
	resolution, err := s.resolveCanonical(ctx, locator)
	if err != nil {
		return TargetPlan{}, err
	}
	prior, err := target.NewDefault(locator, nil)
	if err != nil {
		return TargetPlan{}, err
	}
	return newTargetPlan("target.clear", resolution, &prior, nil), nil
}

// CommitTargetPlan applies one previously prepared immutable authority transition.
func (s *TargetService) CommitTargetPlan(ctx context.Context, plan TargetPlan) (target.Publication, error) {
	if err := validateTargetPlan(plan); err != nil {
		return target.Publication{}, err
	}
	if plan.Desired == nil {
		return s.store.Clear(ctx)
	}
	return s.store.Save(ctx, plan.Desired.Clone())
}

// EnrollDefaultTarget resolves one observed local VM and atomically persists only its canonical identity.
func (s *TargetService) EnrollDefaultTarget(
	ctx context.Context,
	reference string,
	aliases []string,
) (TargetResolution, target.Publication, error) {
	plan, err := s.PrepareEnrollDefaultTarget(ctx, reference, aliases)
	if err != nil {
		return TargetResolution{}, target.Publication{}, err
	}
	publication, err := s.CommitTargetPlan(ctx, plan)
	return plan.Resolution, publication, err
}

// ResolveTarget resolves default, stored alias, or an exact inventory reference to the enrolled identity.
func (s *TargetService) ResolveTarget(ctx context.Context, reference string) (TargetResolution, error) {
	value, err := s.store.Load(ctx)
	if err != nil {
		return TargetResolution{}, err
	}
	if err := s.refreshInventory(ctx); err != nil {
		return TargetResolution{}, err
	}

	locator := value.Locator
	if !isStoredTargetReference(reference, value) {
		if err := s.validateExplicitTargetReference(reference, locator); err != nil {
			return TargetResolution{}, err
		}
	}
	return s.resolveCanonical(ctx, locator)
}

func isStoredTargetReference(reference string, value target.Default) bool {
	return reference == "" || reference == "default" || slices.Contains(value.Aliases, reference)
}

func (s *TargetService) validateExplicitTargetReference(reference string, locator domain.MachineLocator) error {
	if _, err := domain.ParseMachineLocator(reference); err != nil {
		if _, err := domain.NormalizeMachineGUID(reference); err != nil {
			return target.ErrDifferentTarget
		}
	}
	entry, err := s.inventory.ResolveMachine(reference)
	if err != nil {
		return err
	}
	if entry.Locator != locator {
		return target.ErrDifferentTarget
	}
	return nil
}

// ShowDefaultTarget returns the stored default only after inventory proves it remains routeable.
func (s *TargetService) ShowDefaultTarget(ctx context.Context) (TargetResolution, error) {
	return s.ResolveTarget(ctx, "default")
}

// ClearDefaultTarget removes the canonical target authority.
func (s *TargetService) ClearDefaultTarget(ctx context.Context) (target.Publication, error) {
	plan, err := s.PrepareClearDefaultTarget(ctx)
	if err != nil {
		return target.Publication{}, err
	}
	return s.CommitTargetPlan(ctx, plan)
}

func (s *TargetService) refreshInventory(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.refresh(ctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return target.ErrInventoryRefresh
	}
	return ctx.Err()
}

func (s *TargetService) loadOptional(ctx context.Context) (*target.Default, error) {
	value, err := s.store.Load(ctx)
	if errors.Is(err, target.ErrNoDefault) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	clone := value.Clone()
	return &clone, nil
}

func newTargetPlan(kind domain.OperationKind, resolution TargetResolution, prior, desired *target.Default) TargetPlan {
	plan := TargetPlan{
		Kind: kind, Resolution: resolution, Prior: cloneDefault(prior), Desired: cloneDefault(desired),
		StateHash: target.TransitionDigest(prior, desired), PriorHash: target.StateDigest(prior), DesiredHash: target.StateDigest(desired),
	}
	if desired != nil {
		plan.AliasCount = len(desired.Aliases)
	}
	return plan
}

func cloneDefault(value *target.Default) *target.Default {
	if value == nil {
		return nil
	}
	clone := value.Clone()
	return &clone
}

func validateTargetPlan(plan TargetPlan) error {
	if plan.Kind != "target.enroll" && plan.Kind != "target.clear" {
		return domain.ErrInvalidOperationKind
	}
	if err := plan.Resolution.Validate(); err != nil {
		return err
	}
	if plan.StateHash != target.TransitionDigest(plan.Prior, plan.Desired) ||
		plan.PriorHash != target.StateDigest(plan.Prior) || plan.DesiredHash != target.StateDigest(plan.Desired) {
		return errors.New("app: target plan identity mismatch")
	}
	if plan.Kind == "target.enroll" && plan.Desired == nil {
		return errors.New("app: enroll plan requires desired state")
	}
	if plan.Kind == "target.clear" && (plan.Prior == nil || plan.Desired != nil) {
		return errors.New("app: clear plan requires prior state and absent desired state")
	}
	return nil
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
