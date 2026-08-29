package mcpadapter

import (
	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

type DoctorInput struct{}

type DoctorResult struct {
	SchemaVersion string           `json:"schema_version"`
	Status        app.DoctorStatus `json:"status"`
	Ready         bool             `json:"ready"`
	Reason        app.DoctorReason `json:"reason,omitempty"`
	Message       string           `json:"message"`
	Capabilities  []string         `json:"capabilities"`
	ObservedAt    string           `json:"observed_at"`
}

type MachineListInput struct{}

type NetworkAdapterDTO struct {
	Name        string   `json:"name"`
	SwitchName  string   `json:"switch_name,omitempty"`
	MACAddress  string   `json:"mac_address,omitempty"`
	IPAddresses []string `json:"ip_addresses"`
	Status      string   `json:"status,omitempty"`
}

type MachineDTO struct {
	ID                  string              `json:"id"`
	Name                string              `json:"name"`
	State               string              `json:"state"`
	RawState            string              `json:"raw_state"`
	RawStatus           string              `json:"raw_status,omitempty"`
	Generation          int                 `json:"generation"`
	Version             string              `json:"version"`
	UptimeMs            int64               `json:"uptime_ms"`
	CPUUsagePercent     int                 `json:"cpu_usage_percent"`
	MemoryAssignedBytes uint64              `json:"memory_assigned_bytes"`
	NetworkAdapters     []NetworkAdapterDTO `json:"network_adapters"`
	Capabilities        []string            `json:"capabilities"`
	ObservedAt          string              `json:"observed_at"`
	ObservationType     string              `json:"observation_type"`
}

type MachineListResult struct {
	SchemaVersion   string       `json:"schema_version"`
	ObservationType string       `json:"observation_type"`
	Machines        []MachineDTO `json:"machines"`
}

type MachineInspectInput struct {
	ID string `json:"id" jsonschema:"The machine GUID to inspect"`
}

type MachineInspectResult struct {
	SchemaVersion   string     `json:"schema_version"`
	ObservationType string     `json:"observation_type"`
	Machine         MachineDTO `json:"machine"`
}

type CheckpointListInput struct {
	ID string `json:"id" jsonschema:"The machine GUID to list checkpoints for"`
}

type CheckpointDTO struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	VMID            string `json:"vm_id"`
	ParentID        string `json:"parent_id,omitempty"`
	CheckpointType  string `json:"checkpoint_type,omitempty"`
	CreatedAt       string `json:"created_at"`
	ObservedAt      string `json:"observed_at"`
	ObservationType string `json:"observation_type"`
}

type CheckpointListResult struct {
	SchemaVersion   string          `json:"schema_version"`
	ObservationType string          `json:"observation_type"`
	Checkpoints     []CheckpointDTO `json:"checkpoints"`
}

type MachineStartInput struct {
	ID             string `json:"id" jsonschema:"The machine GUID to start"`
	Reason         string `json:"reason" jsonschema:"Reason for starting the machine"`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Idempotency key for the operation"`
	Timeout        string `json:"timeout" jsonschema:"Explicit timeout duration (e.g. 5m)"`
}

type MachineMutationResult struct {
	SchemaVersion string      `json:"schema_version"`
	Receipt       receipt.DTO `json:"receipt"`
	Machine       *MachineDTO `json:"machine,omitempty"`
}

type MachineStopInput struct {
	ID             string `json:"id" jsonschema:"The machine GUID to stop"`
	Mode           string `json:"mode" jsonschema:"Stop mode (e.g. shutdown, save, or turn-off)"`
	Reason         string `json:"reason" jsonschema:"Reason for stopping the machine"`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Idempotency key for the operation"`
	Timeout        string `json:"timeout" jsonschema:"Explicit timeout duration (e.g. 5m)"`
}

type CheckpointCreateInput struct {
	ID             string `json:"id" jsonschema:"The machine GUID to checkpoint"`
	Name           string `json:"name" jsonschema:"Name of the new checkpoint"`
	Reason         string `json:"reason" jsonschema:"Reason for creating the checkpoint"`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Idempotency key for the operation"`
	Timeout        string `json:"timeout" jsonschema:"Explicit timeout duration (e.g. 5m)"`
}

type CheckpointMutationResult struct {
	SchemaVersion string         `json:"schema_version"`
	Receipt       receipt.DTO    `json:"receipt"`
	Checkpoint    *CheckpointDTO `json:"checkpoint,omitempty"`
	Machine       *MachineDTO    `json:"machine,omitempty"`
}

type CheckpointRestoreInput struct {
	ID             string `json:"id" jsonschema:"The machine GUID to restore"`
	CheckpointID   string `json:"checkpoint_id" jsonschema:"The checkpoint GUID to restore"`
	Reason         string `json:"reason" jsonschema:"Reason for restoring the checkpoint"`
	IdempotencyKey string `json:"idempotency_key" jsonschema:"Idempotency key for the operation"`
	Timeout        string `json:"timeout" jsonschema:"Explicit timeout duration (e.g. 5m)"`
}

type OperationListInput struct {
	State   string `json:"state,omitempty" jsonschema:"Filter by operation state"`
	Machine string `json:"machine,omitempty" jsonschema:"Filter by machine GUID"`
	Limit   int    `json:"limit,omitempty" jsonschema:"Limit the number of operations returned"`
}

type OperationListResult struct {
	SchemaVersion string                `json:"schema_version"`
	Operations    []daemon.OperationDTO `json:"operations"`
}

type OperationShowInput struct {
	OperationID string `json:"operation_id" jsonschema:"The operation ID to show"`
}

type OperationResult struct {
	SchemaVersion string               `json:"schema_version"`
	Operation     *daemon.OperationDTO `json:"operation"`
}

type OperationWaitInput struct {
	OperationID string `json:"operation_id" jsonschema:"The operation ID to wait for"`
	Timeout     string `json:"timeout,omitempty" jsonschema:"Explicit wait timeout duration (e.g. 5m)"`
	AfterSeq    string `json:"after_seq,omitempty" jsonschema:"Wait for events after sequence number"`
}

type ReceiptShowInput struct {
	ReceiptID string `json:"receipt_id" jsonschema:"The receipt ID to show"`
}

type ReceiptResult struct {
	SchemaVersion string      `json:"schema_version"`
	Receipt       receipt.DTO `json:"receipt"`
}
