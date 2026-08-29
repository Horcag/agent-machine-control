package app

import (
	"context"
	"errors"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

// MachineObserver provides read-only observation queries against host hypervisors.
type MachineObserver interface {
	Doctor(ctx context.Context) (DoctorReport, error)
	ListMachines(ctx context.Context) ([]domain.MachineObservation, error)
	InspectMachine(ctx context.Context, id string) (domain.MachineObservation, error)
}

// DiscoveryService orchestrates read-only VM inspection and provider diagnostics.
type DiscoveryService struct {
	observer MachineObserver
}

// NewDiscoveryService creates a new DiscoveryService backed by a MachineObserver.
func NewDiscoveryService(observer MachineObserver) *DiscoveryService {
	return &DiscoveryService{observer: observer}
}

// Doctor returns the current operational readiness and diagnostics of the machine provider.
func (s *DiscoveryService) Doctor(ctx context.Context) (DoctorReport, error) {
	if s.observer == nil {
		return DoctorReport{}, errors.New("app: no machine observer configured")
	}
	return s.observer.Doctor(ctx)
}

// List returns all discovered virtual machines.
func (s *DiscoveryService) List(ctx context.Context) ([]domain.MachineObservation, error) {
	if s.observer == nil {
		return nil, errors.New("app: no machine observer configured")
	}
	return s.observer.ListMachines(ctx)
}

// Inspect returns detailed observation data for a specific virtual machine by its GUID.
func (s *DiscoveryService) Inspect(ctx context.Context, id string) (domain.MachineObservation, error) {
	if s.observer == nil {
		return domain.MachineObservation{}, errors.New("app: no machine observer configured")
	}
	if err := domain.ValidateMachineGUID(id); err != nil {
		return domain.MachineObservation{}, err
	}
	return s.observer.InspectMachine(ctx, id)
}
