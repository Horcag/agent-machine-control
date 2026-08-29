package hyperv

import "errors"

var (
	// ErrExecutableNotFound indicates powershell.exe was not found in PATH.
	ErrExecutableNotFound = errors.New("hyperv: powershell executable not found")

	// ErrCommandTimeout indicates execution exceeded the configured context deadline.
	ErrCommandTimeout = errors.New("hyperv: command execution timed out")

	// ErrOutputExceededLimit indicates process output exceeded stdout/stderr size bounds.
	ErrOutputExceededLimit = errors.New("hyperv: command output exceeded size limit")

	// ErrBackendUnavailable indicates Hyper-V host management is unavailable.
	ErrBackendUnavailable = errors.New("hyperv: backend is unavailable")

	// ErrAccessDenied indicates insufficient permissions to access Hyper-V.
	ErrAccessDenied = errors.New("hyperv: access denied to hyper-v host")

	// ErrModuleMissing indicates the Hyper-V PowerShell module is not installed.
	ErrModuleMissing = errors.New("hyperv: hyper-v powershell module not found")

	// ErrHostUnavailable indicates the Hyper-V management service is not responding.
	ErrHostUnavailable = errors.New("hyperv: hyper-v management host service is unavailable")

	// ErrMachineNotFound indicates the requested virtual machine was not found.
	ErrMachineNotFound = errors.New("hyperv: virtual machine not found")

	// ErrMalformedResponse indicates PowerShell returned unparseable or invalid JSON.
	ErrMalformedResponse = errors.New("hyperv: malformed provider response")

	// ErrUnexpectedSchemaVersion indicates the envelope version does not match expected version.
	ErrUnexpectedSchemaVersion = errors.New("hyperv: unexpected response schema version")

	// ErrTrailingData indicates trailing garbage or extra JSON tokens after the envelope.
	ErrTrailingData = errors.New("hyperv: trailing data detected after JSON envelope")

	// ErrDuplicateMachineID indicates duplicate VM GUIDs in the list response.
	ErrDuplicateMachineID = errors.New("hyperv: duplicate machine ID observed")

	// ErrCheckpointNotFound indicates the requested checkpoint was not found.
	ErrCheckpointNotFound = errors.New("hyperv: checkpoint not found")

	// ErrInvalidState indicates the machine is not in a valid state for the requested operation.
	ErrInvalidState = errors.New("hyperv: machine is in an invalid state for operation")
)
