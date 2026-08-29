package hyperv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

type checkpointListEnvelope struct {
	SchemaVersion string          `json:"schema_version"`
	Checkpoints   json.RawMessage `json:"checkpoints,omitempty"`
	ErrorCategory string          `json:"error_category,omitempty"`
}

type checkpointCreateEnvelope struct {
	SchemaVersion string          `json:"schema_version"`
	Success       *bool           `json:"success,omitempty"`
	Checkpoint    json.RawMessage `json:"checkpoint,omitempty"`
	ErrorCategory string          `json:"error_category,omitempty"`
}

type rawCheckpoint struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	VMID           string `json:"vm_id"`
	ParentID       string `json:"parent_id,omitempty"`
	CheckpointType string `json:"checkpoint_type,omitempty"`
	CreationTime   string `json:"creation_time"`
}

func parseCheckpointListResponse(stdout []byte, observedAt time.Time) ([]domain.CheckpointObservation, error) {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return nil, ErrMalformedResponse
	}

	var env checkpointListEnvelope
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformedResponse, err)
	}

	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, ErrTrailingData
	}

	if env.SchemaVersion != ExpectedSchemaVersion {
		return nil, fmt.Errorf("%w: got %q", ErrUnexpectedSchemaVersion, env.SchemaVersion)
	}

	if err := mapEnvelopeError(env.ErrorCategory); err != nil {
		return nil, err
	}

	if len(env.Checkpoints) == 0 {
		return []domain.CheckpointObservation{}, nil
	}

	rawList, err := normalizeCheckpointList(env.Checkpoints)
	if err != nil {
		return nil, err
	}

	results := make([]domain.CheckpointObservation, len(rawList))
	for i, raw := range rawList {
		obs, err := convertRawCheckpoint(raw, observedAt)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid checkpoint item: %v", ErrMalformedResponse, err)
		}
		results[i] = obs
	}

	return results, nil
}

func parseCheckpointCreateResponse(stdout []byte, observedAt time.Time) (domain.CheckpointObservation, error) {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return domain.CheckpointObservation{}, ErrMalformedResponse
	}

	var env checkpointCreateEnvelope
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil {
		return domain.CheckpointObservation{}, fmt.Errorf("%w: %v", ErrMalformedResponse, err)
	}

	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return domain.CheckpointObservation{}, ErrTrailingData
	}

	if env.SchemaVersion != ExpectedSchemaVersion {
		return domain.CheckpointObservation{}, fmt.Errorf("%w: got %q", ErrUnexpectedSchemaVersion, env.SchemaVersion)
	}

	if err := mapEnvelopeError(env.ErrorCategory); err != nil {
		return domain.CheckpointObservation{}, err
	}

	if env.Success == nil || !*env.Success {
		return domain.CheckpointObservation{}, fmt.Errorf("%w: missing success=true flag", ErrMalformedResponse)
	}

	if len(env.Checkpoint) == 0 {
		return domain.CheckpointObservation{}, fmt.Errorf("%w: missing checkpoint in response", ErrMalformedResponse)
	}

	var raw rawCheckpoint
	if err := decodeStrictJSON(env.Checkpoint, &raw); err != nil {
		return domain.CheckpointObservation{}, fmt.Errorf("%w: %v", ErrMalformedResponse, err)
	}

	return convertRawCheckpoint(raw, observedAt)
}

func normalizeCheckpointList(raw json.RawMessage) ([]rawCheckpoint, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return []rawCheckpoint{}, nil
	}
	if trimmed[0] == '[' {
		var rawList []json.RawMessage
		if err := decodeStrictJSON(trimmed, &rawList); err != nil {
			return nil, fmt.Errorf("%w: failed to decode checkpoints array: %w", ErrMalformedResponse, err)
		}
		list := make([]rawCheckpoint, len(rawList))
		for i, item := range rawList {
			if err := decodeStrictJSON(item, &list[i]); err != nil {
				return nil, fmt.Errorf("%w: invalid checkpoint item: %w", ErrMalformedResponse, err)
			}
		}
		return list, nil
	}
	if trimmed[0] == '{' {
		var single rawCheckpoint
		if err := decodeStrictJSON(trimmed, &single); err != nil {
			return nil, fmt.Errorf("%w: failed to decode single checkpoint: %w", ErrMalformedResponse, err)
		}
		return []rawCheckpoint{single}, nil
	}
	return nil, fmt.Errorf("%w: expected array or object for checkpoints", ErrMalformedResponse)
}

func convertRawCheckpoint(raw rawCheckpoint, observedAt time.Time) (domain.CheckpointObservation, error) {
	var createdAt time.Time
	if raw.CreationTime != "" {
		t, err := time.Parse(time.RFC3339, raw.CreationTime)
		if err != nil {
			t, err = time.Parse(time.RFC3339Nano, raw.CreationTime)
			if err != nil {
				return domain.CheckpointObservation{}, fmt.Errorf("%w: invalid creation_time %q", ErrMalformedResponse, raw.CreationTime)
			}
		}
		createdAt = t.UTC()
	} else {
		createdAt = observedAt
	}

	obs := domain.CheckpointObservation{
		ID:              raw.ID,
		Name:            raw.Name,
		VMID:            raw.VMID,
		ParentID:        raw.ParentID,
		CheckpointType:  raw.CheckpointType,
		CreatedAt:       createdAt,
		ObservedAt:      observedAt,
		ObservationType: domain.ObservationObserved,
	}

	if err := obs.Validate(); err != nil {
		return domain.CheckpointObservation{}, fmt.Errorf("%w: %v", ErrMalformedResponse, err)
	}

	return obs, nil
}
