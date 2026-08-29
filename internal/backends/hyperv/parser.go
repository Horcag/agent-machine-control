package hyperv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

const ExpectedSchemaVersion = "1"

const (
	CategoryModuleMissing   = "module_missing"
	CategoryAccessDenied    = "access_denied"
	CategoryMachineNotFound = "machine_not_found"
	CategoryHostUnavailable = "host_unavailable"
	CategoryTimeout         = "timeout"
)

var validDoctorCategories = map[string]struct{}{
	CategoryModuleMissing:   {},
	CategoryAccessDenied:    {},
	CategoryHostUnavailable: {},
	CategoryTimeout:         {},
}

var validListCategories = map[string]struct{}{
	CategoryModuleMissing:   {},
	CategoryAccessDenied:    {},
	CategoryHostUnavailable: {},
	CategoryTimeout:         {},
}

var validInspectCategories = map[string]struct{}{
	CategoryModuleMissing:   {},
	CategoryAccessDenied:    {},
	CategoryMachineNotFound: {},
	CategoryHostUnavailable: {},
	CategoryTimeout:         {},
}

type doctorEnvelope struct {
	SchemaVersion string `json:"schema_version"`
	Ready         *bool  `json:"ready,omitempty"`
	ErrorCategory string `json:"error_category,omitempty"`
}

type listEnvelope struct {
	SchemaVersion string          `json:"schema_version"`
	Machines      json.RawMessage `json:"machines,omitempty"`
	ErrorCategory string          `json:"error_category,omitempty"`
}

type inspectEnvelope struct {
	SchemaVersion string          `json:"schema_version"`
	Machine       json.RawMessage `json:"machine,omitempty"`
	ErrorCategory string          `json:"error_category,omitempty"`
}

func decodeStrictJSON(data []byte, v any) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return fmt.Errorf("%w: empty payload", ErrMalformedResponse)
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %w", ErrMalformedResponse, err)
	}
	var trailing json.RawMessage
	if dec.More() || (dec.Decode(&trailing) != io.EOF) {
		return ErrTrailingData
	}
	return nil
}

func mapDoctorReason(category string) app.DoctorReason {
	switch category {
	case CategoryModuleMissing:
		return app.DoctorReasonModuleMissing
	case CategoryAccessDenied:
		return app.DoctorReasonAccessDenied
	case CategoryHostUnavailable, CategoryTimeout:
		return app.DoctorReasonHostUnavailable
	case "executable_missing":
		return app.DoctorReasonExecutableMissing
	default:
		return app.DoctorReasonHostUnavailable
	}
}

func mapCategoryToError(cat string) error {
	switch cat {
	case CategoryModuleMissing:
		return ErrModuleMissing
	case CategoryAccessDenied:
		return ErrAccessDenied
	case CategoryMachineNotFound:
		return ErrMachineNotFound
	case CategoryHostUnavailable:
		return ErrHostUnavailable
	case CategoryTimeout:
		return ErrCommandTimeout
	default:
		return fmt.Errorf("%w: unknown error category", ErrMalformedResponse)
	}
}

func parseDoctorResponse(stdout []byte, now time.Time) (app.DoctorReport, error) {
	var env doctorEnvelope
	if err := decodeStrictJSON(stdout, &env); err != nil {
		return app.DoctorReport{}, err
	}
	if env.SchemaVersion != ExpectedSchemaVersion {
		return app.DoctorReport{}, ErrUnexpectedSchemaVersion
	}

	if env.Ready == nil {
		return app.DoctorReport{}, fmt.Errorf("%w: missing ready field in doctor response", ErrMalformedResponse)
	}

	if *env.Ready {
		if env.ErrorCategory != "" {
			return app.DoctorReport{}, fmt.Errorf("%w: ready doctor response cannot specify error_category", ErrMalformedResponse)
		}
		return app.NewReadyReport(domain.ReadOnlyMachineCapabilities(), now), nil
	}

	if env.ErrorCategory == "" {
		return app.DoctorReport{}, fmt.Errorf("%w: missing error_category in unavailable doctor response", ErrMalformedResponse)
	}
	if _, ok := validDoctorCategories[env.ErrorCategory]; !ok {
		return app.DoctorReport{}, fmt.Errorf("%w: invalid error_category in doctor response", ErrMalformedResponse)
	}

	reason := mapDoctorReason(env.ErrorCategory)
	var msg string
	switch reason {
	case app.DoctorReasonModuleMissing:
		msg = "Hyper-V PowerShell module is not installed or accessible"
	case app.DoctorReasonAccessDenied:
		msg = "Access to Hyper-V host management was denied"
	case app.DoctorReasonExecutableMissing:
		msg = "PowerShell executable (powershell.exe) was not found in PATH"
	default:
		msg = "Hyper-V management service is unavailable"
	}
	return app.NewUnavailableReport(reason, msg, now), nil
}

func parseListResponse(stdout []byte, now time.Time) ([]domain.MachineObservation, error) {
	var env listEnvelope
	if err := decodeStrictJSON(stdout, &env); err != nil {
		return nil, err
	}
	if env.SchemaVersion != ExpectedSchemaVersion {
		return nil, ErrUnexpectedSchemaVersion
	}

	hasMachines := len(env.Machines) > 0
	hasCategory := env.ErrorCategory != ""

	if hasMachines && hasCategory {
		return nil, fmt.Errorf("%w: list response cannot specify both machines and error_category", ErrMalformedResponse)
	}
	if !hasMachines && !hasCategory {
		return nil, fmt.Errorf("%w: list response missing machines or error_category", ErrMalformedResponse)
	}

	if hasCategory {
		if _, ok := validListCategories[env.ErrorCategory]; !ok {
			return nil, fmt.Errorf("%w: invalid error_category in list response", ErrMalformedResponse)
		}
		return nil, mapCategoryToError(env.ErrorCategory)
	}

	rawItems, err := normalizeMachineList(env.Machines)
	if err != nil {
		return nil, err
	}

	seenIDs := make(map[string]struct{}, len(rawItems))
	result := make([]domain.MachineObservation, 0, len(rawItems))

	for _, raw := range rawItems {
		if _, exists := seenIDs[raw.ID]; exists {
			return nil, ErrDuplicateMachineID
		}
		seenIDs[raw.ID] = struct{}{}

		obs, err := convertRawMachine(raw, now)
		if err != nil {
			return nil, err
		}
		result = append(result, obs)
	}

	return result, nil
}

func parseInspectResponse(stdout []byte, now time.Time) (domain.MachineObservation, error) {
	var env inspectEnvelope
	if err := decodeStrictJSON(stdout, &env); err != nil {
		return domain.MachineObservation{}, err
	}
	if env.SchemaVersion != ExpectedSchemaVersion {
		return domain.MachineObservation{}, ErrUnexpectedSchemaVersion
	}

	hasMachine := len(env.Machine) > 0
	hasCategory := env.ErrorCategory != ""

	if hasMachine && hasCategory {
		return domain.MachineObservation{}, fmt.Errorf("%w: inspect response cannot specify both machine and error_category", ErrMalformedResponse)
	}
	if !hasMachine && !hasCategory {
		return domain.MachineObservation{}, fmt.Errorf("%w: inspect response missing machine or error_category", ErrMalformedResponse)
	}

	if hasCategory {
		if _, ok := validInspectCategories[env.ErrorCategory]; !ok {
			return domain.MachineObservation{}, fmt.Errorf("%w: invalid error_category in inspect response", ErrMalformedResponse)
		}
		return domain.MachineObservation{}, mapCategoryToError(env.ErrorCategory)
	}

	var raw rawMachine
	if err := decodeStrictJSON(env.Machine, &raw); err != nil {
		return domain.MachineObservation{}, fmt.Errorf("%w: invalid machine detail payload: %w", ErrMalformedResponse, err)
	}

	return convertRawMachine(raw, now)
}
