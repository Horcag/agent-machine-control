package app

import (
	"context"
	"strings"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/policy"
)

// SafetyResolution holds the dynamic safety evaluation result for a machine target.
type SafetyResolution struct {
	Classification domain.OperationClass
	RollbackState  policy.RollbackState
	RollbackRef    string
	Contained      bool
}

// MachineSafetyConfig is the backend-neutral server-owned containment policy.
type MachineSafetyConfig struct {
	ExternalEffectsContained    bool
	RollbackCheckpointID        string
	RequireProductionCheckpoint bool
}

// MachineConfigLoader abstracts reading only server-owned safety configuration.
type MachineConfigLoader interface {
	GetMachineSafetyConfig(target domain.MachineRef) (*MachineSafetyConfig, error)
}

// SafetyResolver determines dynamic safety classification for session operations on target VMs.
type SafetyResolver interface {
	ResolveSafety(ctx context.Context, target domain.MachineRef) (SafetyResolution, error)
}

// DefaultSafetyResolver verifies server-owned configuration and backend checkpoints.
type DefaultSafetyResolver struct {
	configLoader MachineConfigLoader
	backend      Backend
}

// NewDefaultSafetyResolver creates a DefaultSafetyResolver.
func NewDefaultSafetyResolver(configLoader MachineConfigLoader, backend Backend) *DefaultSafetyResolver {
	return &DefaultSafetyResolver{
		configLoader: configLoader,
		backend:      backend,
	}
}

// ResolveSafety checks if the target has external effects contained and an active verified checkpoint.
func (r *DefaultSafetyResolver) ResolveSafety(ctx context.Context, target domain.MachineRef) (SafetyResolution, error) {
	destructive := SafetyResolution{
		Classification: domain.ClassDestructivePrivileged,
		RollbackState:  policy.RollbackState{Available: false, Verified: false},
		RollbackRef:    "",
		Contained:      false,
	}

	if r.configLoader == nil || r.backend == nil {
		return destructive, nil
	}

	cfg, err := r.configLoader.GetMachineSafetyConfig(target)
	if err != nil || cfg == nil {
		return destructive, nil
	}

	if !cfg.ExternalEffectsContained || cfg.RollbackCheckpointID == "" {
		return destructive, nil
	}

	checkpoints, err := r.backend.ListCheckpoints(ctx, string(target))
	if err != nil || len(checkpoints) == 0 {
		return destructive, nil
	}

	checkpointByID := make(map[string]domain.CheckpointObservation, len(checkpoints))
	for _, checkpoint := range checkpoints {
		checkpointByID[checkpoint.ID] = checkpoint
	}
	checkpoint, ok := checkpointByID[cfg.RollbackCheckpointID]
	if !ok || !validCheckpointChain(checkpoint, checkpointByID, string(target), cfg.RequireProductionCheckpoint) {
		return destructive, nil
	}

	return SafetyResolution{
		Classification: domain.ClassReversibleMutation,
		RollbackState: policy.RollbackState{
			Available:    true,
			Verified:     true,
			CheckpointID: cfg.RollbackCheckpointID,
		},
		RollbackRef: cfg.RollbackCheckpointID,
		Contained:   true,
	}, nil
}

func validCheckpointChain(checkpoint domain.CheckpointObservation, checkpoints map[string]domain.CheckpointObservation, target string, requireProduction bool) bool {
	seen := make(map[string]struct{})
	for {
		if _, duplicate := seen[checkpoint.ID]; duplicate {
			return false
		}
		seen[checkpoint.ID] = struct{}{}
		if checkpoint.Validate() != nil || checkpoint.VMID != target || checkpoint.CheckpointType == "" || strings.EqualFold(checkpoint.CheckpointType, "Microsoft:Hyper-V:Snapshot:Missing") {
			return false
		}
		if requireProduction && !strings.EqualFold(checkpoint.CheckpointType, "Production") {
			return false
		}
		if checkpoint.ParentID == "" {
			return true
		}
		parent, ok := checkpoints[checkpoint.ParentID]
		if !ok {
			return false
		}
		checkpoint = parent
	}
}
