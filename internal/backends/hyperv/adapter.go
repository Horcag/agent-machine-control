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

// HostAddressEnvVar is the environment variable name used to pass an explicit trusted host address.
const HostAddressEnvVar = "AMC_HYPERV_HOST_ADDRESS"

// HostRoute is an immutable Hyper-V host route owned by an adapter instance.
type HostRoute struct {
	HostID  domain.HostID
	Address string
	Remote  bool
}

// LocalHostRoute returns the automatic local Hyper-V host route.
func LocalHostRoute() HostRoute {
	return HostRoute{HostID: domain.LocalHostID}
}

// ExplicitRemoteHostRoute validates an operator-supplied trusted host entry for remote routing.
func ExplicitRemoteHostRoute(host app.HostEntry) (HostRoute, error) {
	if err := host.Validate(); err != nil {
		return HostRoute{}, err
	}
	if host.ID == domain.LocalHostID {
		return HostRoute{}, fmt.Errorf("%w: local host ID cannot be used for explicit remote route", domain.ErrInvalidHostID)
	}
	return HostRoute{HostID: host.ID, Address: host.Address, Remote: true}, nil
}

func (r HostRoute) validate() error {
	if r.HostID == "" {
		r.HostID = domain.LocalHostID
	}
	if err := r.HostID.Validate(); err != nil {
		return err
	}
	if r.Remote {
		return domain.ValidateHostAddress(r.Address)
	}
	if r.Address != "" {
		return fmt.Errorf("%w: local route cannot carry a remote address", domain.ErrInvalidHostAddress)
	}
	return nil
}

// Adapter provides read-only observation queries against local Hyper-V via PowerShell.
type Adapter struct {
	executor Executor
	exePath  string
	nowFn    func() time.Time
	route    HostRoute
}

// Option configures the Hyper-V Adapter.
type Option func(*Adapter)

// WithExecutor configures a custom process executor.
func WithExecutor(exec Executor) Option {
	return func(a *Adapter) {
		if exec != nil {
			a.executor = exec
		}
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

// WithHostRoute configures the immutable Hyper-V host route for this adapter.
func WithHostRoute(route HostRoute) Option {
	return func(a *Adapter) {
		a.route = route
	}
}

// New creates a new Hyper-V Adapter.
func New(opts ...Option) *Adapter {
	a := &Adapter{
		executor: &DefaultExecutor{},
		nowFn:    time.Now,
		route:    LocalHostRoute(),
	}
	for _, opt := range opts {
		opt(a)
	}
	if a.executor == nil {
		a.executor = &DefaultExecutor{}
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
	if a.executor == nil {
		a.executor = &DefaultExecutor{}
	}
	if a.exePath != "" {
		return a.exePath, nil
	}
	path, err := a.executor.LookPath("powershell.exe")
	if err != nil {
		return "", ErrExecutableNotFound
	}
	return path, nil
}

func (a *Adapter) command(script string, env []string) ([]string, []string, error) {
	route := a.route
	if route.HostID == "" {
		route = LocalHostRoute()
	}
	if err := route.validate(); err != nil {
		return nil, nil, err
	}
	if route.Remote {
		env = append([]string{fmt.Sprintf("%s=%s", HostAddressEnvVar, route.Address)}, env...)
		script = remoteScript(script)
	}
	return []string{"-NoProfile", "-NonInteractive", "-NoLogo", "-OutputFormat", "Text", "-Command", script}, env, nil
}

// Doctor inspects the availability of PowerShell and the Hyper-V provider.
func (a *Adapter) Doctor(ctx context.Context) (app.DoctorReport, error) {
	now := a.now()
	args, env, err := a.command(ScriptDoctor, nil)
	if err != nil {
		return app.NewUnavailableReport(
			app.DoctorReasonHostUnavailable,
			"Hyper-V host route is invalid",
			now,
		), nil
	}
	exe, err := a.resolveExecutable()
	if err != nil {
		return app.NewUnavailableReport(
			app.DoctorReasonExecutableMissing,
			"PowerShell executable (powershell.exe) was not found in PATH",
			now,
		), nil
	}
	stdout, _, runErr := a.executor.Execute(ctx, exe, args, env)

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
	args, env, err := a.command(ScriptList, nil)
	if err != nil {
		return nil, err
	}
	exe, err := a.resolveExecutable()
	if err != nil {
		return nil, ErrExecutableNotFound
	}
	stdout, _, runErr := a.executor.Execute(ctx, exe, args, env)

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

	machines, err := parseListResponse(stdout, now)
	if err != nil {
		return nil, err
	}
	return a.withRoute(machines)
}

// InspectMachine returns detailed observations for a single virtual machine by GUID.
func (a *Adapter) InspectMachine(ctx context.Context, id string) (domain.MachineObservation, error) {
	now := a.now()
	if err := domain.ValidateMachineGUID(id); err != nil {
		return domain.MachineObservation{}, err
	}

	normalizedID, err := domain.NormalizeMachineGUID(id)
	if err != nil {
		return domain.MachineObservation{}, err
	}
	args, env, err := a.command(ScriptInspect, []string{fmt.Sprintf("%s=%s", TargetVMIDEnvVar, normalizedID)})
	if err != nil {
		return domain.MachineObservation{}, err
	}
	exe, err := a.resolveExecutable()
	if err != nil {
		return domain.MachineObservation{}, ErrExecutableNotFound
	}
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

	machine, err := parseInspectResponse(stdout, now)
	if err != nil {
		return domain.MachineObservation{}, err
	}
	machines, err := a.withRoute([]domain.MachineObservation{machine})
	if err != nil {
		return domain.MachineObservation{}, err
	}
	return machines[0], nil
}

func (a *Adapter) withRoute(machines []domain.MachineObservation) ([]domain.MachineObservation, error) {
	route := a.route
	if route.HostID == "" {
		route = LocalHostRoute()
	}
	if err := route.validate(); err != nil {
		return nil, err
	}
	for idx := range machines {
		normalizedID, err := domain.NormalizeMachineGUID(machines[idx].ID)
		if err != nil {
			return nil, err
		}
		locator, err := domain.NewMachineLocator(route.HostID, normalizedID)
		if err != nil {
			return nil, err
		}
		machines[idx].ID = normalizedID
		machines[idx].HostID = route.HostID
		machines[idx].Locator = locator
	}
	return machines, nil
}
