package cli

import (
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

// CheckpointOutputDTO defines the structured JSON output for a checkpoint.
type CheckpointOutputDTO struct {
	ID              string                 `json:"id"`
	Name            string                 `json:"name"`
	VMID            string                 `json:"vm_id"`
	ParentID        string                 `json:"parent_id,omitempty"`
	CheckpointType  string                 `json:"checkpoint_type,omitempty"`
	CreatedAt       string                 `json:"created_at"`
	ObservedAt      string                 `json:"observed_at"`
	ObservationType domain.ObservationType `json:"observation_type"`
}

// CheckpointListOutputEnvelope defines the structured JSON output for `amc checkpoint list <guid> --json`.
type CheckpointListOutputEnvelope struct {
	SchemaVersion   string                 `json:"schema_version"`
	ObservationType domain.ObservationType `json:"observation_type"`
	Checkpoints     []CheckpointOutputDTO  `json:"checkpoints"`
}

// MachineMutationOutputEnvelope defines the structured JSON output for mutating machine commands.
type MachineMutationOutputEnvelope struct {
	SchemaVersion string            `json:"schema_version"`
	Receipt       receipt.DTO       `json:"receipt"`
	Machine       *MachineOutputDTO `json:"machine,omitempty"`
}

// CheckpointMutationOutputEnvelope defines the structured JSON output for mutating checkpoint commands.
type CheckpointMutationOutputEnvelope struct {
	SchemaVersion string               `json:"schema_version"`
	Receipt       receipt.DTO          `json:"receipt"`
	Checkpoint    *CheckpointOutputDTO `json:"checkpoint,omitempty"`
	Machine       *MachineOutputDTO    `json:"machine,omitempty"`
}

// ConvertToCheckpointDTO converts a domain.CheckpointObservation into a CheckpointOutputDTO.
func ConvertToCheckpointDTO(c domain.CheckpointObservation) CheckpointOutputDTO {
	return CheckpointOutputDTO{
		ID:              c.ID,
		Name:            c.Name,
		VMID:            c.VMID,
		ParentID:        c.ParentID,
		CheckpointType:  c.CheckpointType,
		CreatedAt:       c.CreatedAt.UTC().Format(time.RFC3339),
		ObservedAt:      c.ObservedAt.UTC().Format(time.RFC3339),
		ObservationType: c.ObservationType,
	}
}
