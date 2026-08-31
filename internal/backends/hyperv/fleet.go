package hyperv

import (
	"context"
	"errors"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

const DefaultFleetConcurrency = 4

// HostObserverFactoryOption configures Hyper-V host-scoped observers.
type HostObserverFactoryOption func(*hostObserverFactoryConfig)

type hostObserverFactoryConfig struct {
	executor Executor
	nowFn    func() time.Time
}

// WithHostObserverExecutor configures the executor shared by host-scoped adapters.
func WithHostObserverExecutor(exec Executor) HostObserverFactoryOption {
	return func(c *hostObserverFactoryConfig) {
		if exec != nil {
			c.executor = exec
		}
	}
}

// WithHostObserverNowFunc configures the clock shared by host-scoped adapters.
func WithHostObserverNowFunc(fn func() time.Time) HostObserverFactoryOption {
	return func(c *hostObserverFactoryConfig) {
		if fn != nil {
			c.nowFn = fn
		}
	}
}

// NewHostObserverFactory returns the Hyper-V execution path for app-owned trusted-host refresh.
func NewHostObserverFactory(opts ...HostObserverFactoryOption) app.HostObserverFactory {
	config := hostObserverFactoryConfig{
		executor: &DefaultExecutor{},
		nowFn:    time.Now,
	}
	for _, opt := range opts {
		opt(&config)
	}
	if config.executor == nil {
		config.executor = &DefaultExecutor{}
	}
	return func(host app.HostEntry) app.TrustedHostObserver {
		route, err := routeForHost(host)
		if err != nil {
			return errorObserver{err: err}
		}
		return hostObserver{adapter: New(WithExecutor(config.executor), WithHostRoute(route), WithNowFunc(config.nowFn))}
	}
}

// RefreshTrustedHosts delegates bounded orchestration to app.RefreshTrustedInventory.
func RefreshTrustedHosts(ctx context.Context, inv *app.TrustedInventory, concurrency int, opts ...HostObserverFactoryOption) ([]app.HostSnapshot, error) {
	return app.RefreshTrustedInventory(ctx, inv, NewHostObserverFactory(opts...), concurrency)
}

func routeForHost(host app.HostEntry) (HostRoute, error) {
	if host.ID == "" {
		return HostRoute{}, errors.New("hyperv: missing host ID")
	}
	if host.ID == domain.LocalHostID && host.Address == "local" {
		return LocalHostRoute(), nil
	}
	return ExplicitRemoteHostRoute(host)
}

type errorObserver struct {
	err error
}

func (e errorObserver) ListMachines(context.Context) ([]domain.MachineObservation, error) {
	return nil, e.err
}

type hostObserver struct {
	adapter *Adapter
}

func (h hostObserver) ListMachines(ctx context.Context) ([]domain.MachineObservation, error) {
	machines, err := h.adapter.ListMachines(ctx)
	if err == nil {
		return machines, nil
	}
	switch {
	case errors.Is(err, ErrAccessDenied):
		return nil, domain.ErrMachineAccessDenied
	case errors.Is(err, ErrHostUnavailable), errors.Is(err, ErrCommandTimeout), errors.Is(err, ErrExecutableNotFound), errors.Is(err, ErrModuleMissing):
		return nil, domain.ErrMachineHostUnavailable
	default:
		return nil, err
	}
}
