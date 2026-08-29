package hyperv

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

// TargetVMIDEnvVar is the environment variable name used to pass target VM GUID to inspect script.
const TargetVMIDEnvVar = "AMC_TARGET_VM_ID"

// Adapter provides read-only observation queries against local Hyper-V via PowerShell.
type Adapter struct {
	executor Executor
	exePath  string
	nowFn    func() time.Time
}

// Option configures the Hyper-V Adapter.
type Option func(*Adapter)

// WithExecutor configures a custom process executor.
func WithExecutor(exec Executor) Option {
	return func(a *Adapter) {
		a.executor = exec
	}
}

// WithExecutablePath configures an explicit path to powershell.exe.
func WithExecutablePath(path string) Option {
	return func(a *Adapter) {
		a.exePath = path
	}
}

// WithNowFunc configures a custom clock for timestamps.
func WithNowFunc(fn func() time.Time) Option {
	return func(a *Adapter) {
		a.nowFn = fn
	}
}

// New creates a new Hyper-V Adapter.
func New(opts ...Option) *Adapter {
	a := &Adapter{
		executor: &DefaultExecutor{},
		nowFn:    time.Now,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

func (a *Adapter) now() time.Time {
	if a.nowFn != nil {
		return a.nowFn().UTC()
	}
	return time.Now().UTC()
}

func (a *Adapter) resolveExecutable() (string, error) {
	if a.exePath != "" {
		return a.exePath, nil
	}
	path, err := a.executor.LookPath("powershell.exe")
	if err != nil {
		return "", ErrExecutableNotFound
	}
	return path, nil
}

// Doctor inspects the availability of PowerShell and the Hyper-V provider.
func (a *Adapter) Doctor(ctx context.Context) (app.DoctorReport, error) {
	now := a.now()
	exe, err := a.resolveExecutable()
	if err != nil {
		return app.NewUnavailableReport(
			app.DoctorReasonExecutableMissing,
			"PowerShell executable (powershell.exe) was not found in PATH",
			now,
		), nil
	}

	args := []string{"-NoProfile", "-NonInteractive", "-NoLogo", "-OutputFormat", "Text", "-Command", ScriptDoctor}
	stdout, _, runErr := a.executor.Execute(ctx, exe, args, nil)

	if errors.Is(runErr, ErrOutputExceededLimit) {
		return app.NewUnavailableReport(
			app.DoctorReasonMalformedOutput,
			"Malformed or unexpected response from Hyper-V provider",
			now,
		), nil
	}

	if ctx.Err() != nil || errors.Is(runErr, ErrCommandTimeout) {
		return app.NewUnavailableReport(
			app.DoctorReasonHostUnavailable,
			"Hyper-V management service is unavailable",
			now,
		), nil
	}

	if runErr != nil {
		return app.NewUnavailableReport(
			app.DoctorReasonHostUnavailable,
			"Hyper-V management service is unavailable",
			now,
		), nil
	}

	if len(stdout) == 0 {
		return app.NewUnavailableReport(
			app.DoctorReasonMalformedOutput,
			"Malformed or unexpected response from Hyper-V provider",
			now,
		), nil
	}

	report, parseErr := parseDoctorResponse(stdout, now)
	if parseErr != nil {
		return app.NewUnavailableReport(
			app.DoctorReasonMalformedOutput,
			"Malformed or unexpected response from Hyper-V provider",
			now,
		), nil
	}
	return report, nil
}

// ListMachines discovers all virtual machines on the local Hyper-V host.
func (a *Adapter) ListMachines(ctx context.Context) ([]domain.MachineObservation, error) {
	now := a.now()
	exe, err := a.resolveExecutable()
	if err != nil {
		return nil, ErrExecutableNotFound
	}

	args := []string{"-NoProfile", "-NonInteractive", "-NoLogo", "-OutputFormat", "Text", "-Command", ScriptList}
	stdout, _, runErr := a.executor.Execute(ctx, exe, args, nil)

	if errors.Is(runErr, ErrOutputExceededLimit) {
		return nil, ErrOutputExceededLimit
	}
	if ctx.Err() != nil || errors.Is(runErr, ErrCommandTimeout) {
		return nil, fmt.Errorf("%w: %w", ErrCommandTimeout, ctx.Err())
	}
	if runErr != nil {
		return nil, ErrHostUnavailable
	}

	if len(stdout) == 0 {
		return nil, ErrMalformedResponse
	}

	return parseListResponse(stdout, now)
}

// InspectMachine returns detailed observations for a single virtual machine by GUID.
func (a *Adapter) InspectMachine(ctx context.Context, id string) (domain.MachineObservation, error) {
	now := a.now()
	if err := domain.ValidateMachineGUID(id); err != nil {
		return domain.MachineObservation{}, err
	}

	exe, err := a.resolveExecutable()
	if err != nil {
		return domain.MachineObservation{}, ErrExecutableNotFound
	}

	args := []string{"-NoProfile", "-NonInteractive", "-NoLogo", "-OutputFormat", "Text", "-Command", ScriptInspect}
	env := []string{fmt.Sprintf("%s=%s", TargetVMIDEnvVar, id)}
	stdout, _, runErr := a.executor.Execute(ctx, exe, args, env)

	if errors.Is(runErr, ErrOutputExceededLimit) {
		return domain.MachineObservation{}, ErrOutputExceededLimit
	}
	if ctx.Err() != nil || errors.Is(runErr, ErrCommandTimeout) {
		return domain.MachineObservation{}, fmt.Errorf("%w: %w", ErrCommandTimeout, ctx.Err())
	}
	if runErr != nil {
		return domain.MachineObservation{}, ErrHostUnavailable
	}

	if len(stdout) == 0 {
		return domain.MachineObservation{}, ErrMalformedResponse
	}

	return parseInspectResponse(stdout, now)
}
