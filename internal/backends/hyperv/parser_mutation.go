package hyperv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

const (
	CategoryCheckpointNotFound = "checkpoint_not_found"
	CategoryInvalidState       = "invalid_state"
)

var validMutationCategories = map[string]struct{}{
	CategoryModuleMissing:      {},
	CategoryAccessDenied:       {},
	CategoryMachineNotFound:    {},
	CategoryCheckpointNotFound: {},
	CategoryInvalidState:       {},
	CategoryHostUnavailable:    {},
	CategoryTimeout:            {},
}

func mapEnvelopeError(category string) error {
	if category == "" {
		return nil
	}
	if _, ok := validMutationCategories[category]; !ok {
		return fmt.Errorf("%w: unrecognized error_category %q", ErrMalformedResponse, category)
	}
	switch category {
	case CategoryModuleMissing:
		return ErrModuleMissing
	case CategoryAccessDenied:
		return ErrAccessDenied
	case CategoryMachineNotFound:
		return ErrMachineNotFound
	case CategoryCheckpointNotFound:
		return ErrCheckpointNotFound
	case CategoryInvalidState:
		return ErrInvalidState
	case CategoryTimeout:
		return ErrCommandTimeout
	case CategoryHostUnavailable:
		return ErrHostUnavailable
	default:
		return fmt.Errorf("%w: unrecognized error_category %q", ErrMalformedResponse, category)
	}
}

type mutationEnvelope struct {
	SchemaVersion string          `json:"schema_version"`
	Success       *bool           `json:"success,omitempty"`
	Machine       json.RawMessage `json:"machine,omitempty"`
	ErrorCategory string          `json:"error_category,omitempty"`
}

func parseMutationResponse(stdout []byte, observedAt time.Time) (domain.MachineObservation, error) {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return domain.MachineObservation{}, ErrMalformedResponse
	}

	var env mutationEnvelope
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil {
		return domain.MachineObservation{}, fmt.Errorf("%w: %v", ErrMalformedResponse, err)
	}

	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return domain.MachineObservation{}, ErrTrailingData
	}

	if env.SchemaVersion != ExpectedSchemaVersion {
		return domain.MachineObservation{}, fmt.Errorf("%w: got %q", ErrUnexpectedSchemaVersion, env.SchemaVersion)
	}

	if err := mapEnvelopeError(env.ErrorCategory); err != nil {
		return domain.MachineObservation{}, err
	}

	if env.Success == nil || !*env.Success {
		return domain.MachineObservation{}, fmt.Errorf("%w: missing success=true flag", ErrMalformedResponse)
	}

	if len(env.Machine) == 0 {
		return domain.MachineObservation{}, fmt.Errorf("%w: missing machine observation in mutation response", ErrMalformedResponse)
	}

	var raw rawMachine
	if err := decodeStrictJSON(env.Machine, &raw); err != nil {
		return domain.MachineObservation{}, fmt.Errorf("%w: %v", ErrMalformedResponse, err)
	}

	obs, err := convertRawMachine(raw, observedAt)
	if err != nil {
		return domain.MachineObservation{}, fmt.Errorf("%w: %v", ErrMalformedResponse, err)
	}

	return obs, nil
}
