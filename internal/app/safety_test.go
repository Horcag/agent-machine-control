package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

type mockConfigLoader struct {
	cfg *app.MachineSafetyConfig
	err error
}

func (l *mockConfigLoader) GetMachineSafetyConfig(_ domain.MachineRef) (*app.MachineSafetyConfig, error) {
	return l.cfg, l.err
}

func TestSafetyResolver_ReversibleWhenContainedAndCheckpointVerified(t *testing.T) {
	ctx := context.Background()
	target := domain.MachineRef("c4a523d4-6b99-4d62-a5e2-4752c0f20001")
	chkID := "e4a523d4-6b99-4d62-a5e2-4752c0f20001"

	loader := &mockConfigLoader{
		cfg: &app.MachineSafetyConfig{
			ExternalEffectsContained: true,
			RollbackCheckpointID:     chkID,
		},
	}

	backend := &mockBackend{
		listCheckpointsFn: func(_ context.Context, _ string) ([]domain.CheckpointObservation, error) {
			return []domain.CheckpointObservation{
				{
					ID:              chkID,
					VMID:            string(target),
					Name:            "safe checkpoint",
					CheckpointType:  "Standard",
					CreatedAt:       time.Now().UTC().Add(-time.Hour),
					ObservedAt:      time.Now().UTC(),
					ObservationType: domain.ObservationObserved,
				},
			}, nil
		},
	}

	resolver := app.NewDefaultSafetyResolver(loader, backend)
	res, err := resolver.ResolveSafety(ctx, target)
	if err != nil {
		t.Fatalf("ResolveSafety failed: %v", err)
	}

	if res.Classification != domain.ClassReversibleMutation {
		t.Errorf("expected ClassReversibleMutation, got: %s", res.Classification)
	}
	if !res.Contained || !res.RollbackState.Verified || res.RollbackRef != chkID {
		t.Errorf("unexpected resolution: %+v", res)
	}
}

func TestSafetyResolver_DestructiveWhenNotContained(t *testing.T) {
	ctx := context.Background()
	target := domain.MachineRef("c4a523d4-6b99-4d62-a5e2-4752c0f20001")

	loader := &mockConfigLoader{
		cfg: &app.MachineSafetyConfig{
			ExternalEffectsContained: false,
			RollbackCheckpointID:     "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
		},
	}

	backend := &mockBackend{
		listCheckpointsFn: func(_ context.Context, _ string) ([]domain.CheckpointObservation, error) {
			return []domain.CheckpointObservation{
				{
					ID:              "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
					VMID:            string(target),
					Name:            "safe checkpoint",
					CheckpointType:  "Standard",
					CreatedAt:       time.Now().UTC().Add(-time.Hour),
					ObservedAt:      time.Now().UTC(),
					ObservationType: domain.ObservationObserved,
				},
			}, nil
		},
	}

	resolver := app.NewDefaultSafetyResolver(loader, backend)
	res, err := resolver.ResolveSafety(ctx, target)
	if err != nil {
		t.Fatalf("ResolveSafety failed: %v", err)
	}

	if res.Classification != domain.ClassDestructivePrivileged {
		t.Errorf("expected ClassDestructivePrivileged, got: %s", res.Classification)
	}
	if res.RollbackRef != "" {
		t.Errorf("expected empty RollbackRef, got: %s", res.RollbackRef)
	}
}

func TestSafetyResolver_DestructiveWhenCheckpointMissingOrInvalid(t *testing.T) {
	ctx := context.Background()
	target := domain.MachineRef("c4a523d4-6b99-4d62-a5e2-4752c0f20001")

	loader := &mockConfigLoader{
		cfg: &app.MachineSafetyConfig{
			ExternalEffectsContained: true,
			RollbackCheckpointID:     "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
		},
	}

	backend := &mockBackend{
		listCheckpointsFn: func(_ context.Context, _ string) ([]domain.CheckpointObservation, error) {
			return []domain.CheckpointObservation{
				{
					ID:              "e4a523d4-6b99-4d62-a5e2-4752c0f20002",
					VMID:            string(target),
					Name:            "different checkpoint",
					CheckpointType:  "Standard",
					CreatedAt:       time.Now().UTC().Add(-time.Hour),
					ObservedAt:      time.Now().UTC(),
					ObservationType: domain.ObservationObserved,
				},
			}, nil
		},
	}

	resolver := app.NewDefaultSafetyResolver(loader, backend)
	res, err := resolver.ResolveSafety(ctx, target)
	if err != nil {
		t.Fatalf("ResolveSafety failed: %v", err)
	}

	if res.Classification != domain.ClassDestructivePrivileged {
		t.Errorf("expected ClassDestructivePrivileged for missing checkpoint, got: %s", res.Classification)
	}

	// Backend error -> destructive
	backend.listCheckpointsFn = func(_ context.Context, _ string) ([]domain.CheckpointObservation, error) {
		return nil, errors.New("backend failure")
	}
	res, _ = resolver.ResolveSafety(ctx, target)
	if res.Classification != domain.ClassDestructivePrivileged {
		t.Errorf("expected ClassDestructivePrivileged on backend error, got: %s", res.Classification)
	}
}

func TestSafetyResolver_RejectsUnknownMissingAndBrokenCheckpointChains(t *testing.T) {
	target := domain.MachineRef("c4a523d4-6b99-4d62-a5e2-4752c0f20001")
	checkpointID := "e4a523d4-6b99-4d62-a5e2-4752c0f20001"
	parentID := "e4a523d4-6b99-4d62-a5e2-4752c0f20002"
	now := time.Now().UTC()
	base := domain.CheckpointObservation{
		ID: checkpointID, VMID: string(target), Name: "candidate", CheckpointType: "Standard", CreatedAt: now.Add(-time.Hour), ObservedAt: now, ObservationType: domain.ObservationObserved,
	}
	tests := []struct {
		name        string
		checkpoints []domain.CheckpointObservation
	}{
		{name: "unknown provider status", checkpoints: []domain.CheckpointObservation{func() domain.CheckpointObservation { c := base; c.CheckpointType = ""; return c }()}},
		{name: "blank provider status", checkpoints: []domain.CheckpointObservation{func() domain.CheckpointObservation { c := base; c.CheckpointType = "  "; return c }()}},
		{name: "missing parent", checkpoints: []domain.CheckpointObservation{func() domain.CheckpointObservation { c := base; c.ParentID = parentID; return c }()}},
		{name: "cyclic parent", checkpoints: []domain.CheckpointObservation{
			func() domain.CheckpointObservation { c := base; c.ParentID = parentID; return c }(),
			{ID: parentID, VMID: string(target), ParentID: checkpointID, Name: "parent", CheckpointType: "Standard", CreatedAt: now.Add(-2 * time.Hour), ObservedAt: now, ObservationType: domain.ObservationObserved},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			loader := &mockConfigLoader{cfg: &app.MachineSafetyConfig{ExternalEffectsContained: true, RollbackCheckpointID: checkpointID}}
			backend := &mockBackend{listCheckpointsFn: func(context.Context, string) ([]domain.CheckpointObservation, error) { return tc.checkpoints, nil }}
			resolution, err := app.NewDefaultSafetyResolver(loader, backend).ResolveSafety(context.Background(), target)
			if err != nil || resolution.Classification != domain.ClassDestructivePrivileged {
				t.Fatalf("resolution=%+v err=%v", resolution, err)
			}
		})
	}
}

func TestSafetyResolver_ProductionOnlyRequiresProductionChain(t *testing.T) {
	target := domain.MachineRef("c4a523d4-6b99-4d62-a5e2-4752c0f20001")
	checkpointID := "e4a523d4-6b99-4d62-a5e2-4752c0f20001"
	now := time.Now().UTC()
	checkpointType := "Standard"
	backend := &mockBackend{listCheckpointsFn: func(context.Context, string) ([]domain.CheckpointObservation, error) {
		return []domain.CheckpointObservation{{ID: checkpointID, VMID: string(target), Name: "candidate", CheckpointType: checkpointType, CreatedAt: now.Add(-time.Hour), ObservedAt: now, ObservationType: domain.ObservationObserved}}, nil
	}}
	loader := &mockConfigLoader{cfg: &app.MachineSafetyConfig{ExternalEffectsContained: true, RollbackCheckpointID: checkpointID, RequireProductionCheckpoint: true}}
	resolver := app.NewDefaultSafetyResolver(loader, backend)
	resolution, _ := resolver.ResolveSafety(context.Background(), target)
	if resolution.Classification != domain.ClassDestructivePrivileged {
		t.Fatalf("standard checkpoint satisfied ProductionOnly policy: %+v", resolution)
	}
	checkpointType = "Production"
	resolution, _ = resolver.ResolveSafety(context.Background(), target)
	if resolution.Classification != domain.ClassReversibleMutation {
		t.Fatalf("production checkpoint was not admitted: %+v", resolution)
	}
}
