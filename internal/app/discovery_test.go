package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

type mockObserver struct {
	doctorReport app.DoctorReport
	doctorErr    error
	listResult   []domain.MachineObservation
	listErr      error
	inspectFn    func(ctx context.Context, id string) (domain.MachineObservation, error)
}

func (m *mockObserver) Doctor(_ context.Context) (app.DoctorReport, error) {
	return m.doctorReport, m.doctorErr
}

func (m *mockObserver) ListMachines(_ context.Context) ([]domain.MachineObservation, error) {
	return m.listResult, m.listErr
}

func (m *mockObserver) InspectMachine(ctx context.Context, id string) (domain.MachineObservation, error) {
	if m.inspectFn != nil {
		return m.inspectFn(ctx, id)
	}
	return domain.MachineObservation{}, errors.New("not implemented")
}

func TestDiscoveryService_Doctor(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 29, 14, 0, 0, 0, time.UTC)
	expectedReport := app.NewReadyReport(domain.ReadOnlyMachineCapabilities(), now)

	mock := &mockObserver{
		doctorReport: expectedReport,
	}

	service := app.NewDiscoveryService(mock)
	report, err := service.Doctor(ctx)
	if err != nil {
		t.Fatalf("unexpected Doctor() error: %v", err)
	}
	if report.Status != app.DoctorReady || !report.Ready {
		t.Errorf("expected DoctorReady, got %v", report.Status)
	}
}

func TestDiscoveryService_Doctor_NilObserver(t *testing.T) {
	service := app.NewDiscoveryService(nil)
	_, err := service.Doctor(context.Background())
	if err == nil {
		t.Fatal("expected error with nil observer")
	}
}

func TestDiscoveryService_List(t *testing.T) {
	ctx := context.Background()
	expectedVMs := []domain.MachineObservation{
		{
			ID:   "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
			Name: "vm-1",
		},
	}

	mock := &mockObserver{
		listResult: expectedVMs,
	}

	service := app.NewDiscoveryService(mock)
	vms, err := service.List(ctx)
	if err != nil {
		t.Fatalf("unexpected List() error: %v", err)
	}
	if len(vms) != 1 || vms[0].ID != "c4a523d4-6b99-4d62-a5e2-4752c0f20001" {
		t.Errorf("unexpected List() result: %+v", vms)
	}
}

func TestDiscoveryService_List_NilObserver(t *testing.T) {
	service := app.NewDiscoveryService(nil)
	_, err := service.List(context.Background())
	if err == nil {
		t.Fatal("expected error with nil observer")
	}
}

func TestDiscoveryService_Inspect(t *testing.T) {
	ctx := context.Background()
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

	mock := &mockObserver{
		inspectFn: func(_ context.Context, id string) (domain.MachineObservation, error) {
			if id == targetID {

				return domain.MachineObservation{
					ID:   targetID,
					Name: "found-vm",
				}, nil
			}
			return domain.MachineObservation{}, errors.New("not found")
		},
	}

	service := app.NewDiscoveryService(mock)

	// Valid GUID
	vm, err := service.Inspect(ctx, targetID)
	if err != nil {
		t.Fatalf("unexpected Inspect() error: %v", err)
	}
	if vm.Name != "found-vm" {
		t.Errorf("expected name 'found-vm', got %q", vm.Name)
	}

	// Invalid GUID format
	_, err = service.Inspect(ctx, "not-a-guid")
	if err == nil || !errors.Is(err, domain.ErrInvalidMachineID) {
		t.Fatalf("expected ErrInvalidMachineID for invalid GUID, got %v", err)
	}
}

func TestDiscoveryService_Inspect_NilObserver(t *testing.T) {
	service := app.NewDiscoveryService(nil)
	_, err := service.Inspect(context.Background(), "c4a523d4-6b99-4d62-a5e2-4752c0f20001")
	if err == nil {
		t.Fatal("expected error with nil observer")
	}
}

func TestDoctorReport_Constructors(t *testing.T) {
	now := time.Date(2026, 8, 29, 14, 0, 0, 0, time.UTC)

	t.Run("ready report with nil caps defaults to read-only set", func(t *testing.T) {
		report := app.NewReadyReport(nil, now)
		if !report.Ready || report.Status != app.DoctorReady {
			t.Errorf("expected ready status, got %+v", report)
		}
		if len(report.Capabilities) != 4 {
			t.Errorf("expected 4 capabilities, got %d", len(report.Capabilities))
		}
	})

	t.Run("unavailable report", func(t *testing.T) {
		report := app.NewUnavailableReport(app.DoctorReasonModuleMissing, "module missing", now)
		if report.Ready || report.Status != app.DoctorUnavailable {
			t.Errorf("expected unavailable status, got %+v", report)
		}
		if report.Reason != app.DoctorReasonModuleMissing {
			t.Errorf("expected DoctorReasonModuleMissing, got %v", report.Reason)
		}
		if len(report.Capabilities) != 0 {
			t.Errorf("expected empty capabilities, got %d", len(report.Capabilities))
		}
	})
}

func TestDiscoveryService_NilObserver(t *testing.T) {
	svc := app.NewDiscoveryService(nil)
	ctx := context.Background()

	if _, err := svc.Doctor(ctx); err == nil {
		t.Errorf("expected error for nil observer in Doctor")
	}
	if _, err := svc.List(ctx); err == nil {
		t.Errorf("expected error for nil observer in List")
	}
	if _, err := svc.Inspect(ctx, "c4a523d4-6b99-4d62-a5e2-4752c0f20001"); err == nil {
		t.Errorf("expected error for nil observer in Inspect")
	}
}
