package daemon

import (
	"context"
	"io"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
)

type guestDialSnapshotKey struct{}
type guestDialSnapshot struct {
	target domain.MachineRef
	keys   *frozenGuestKeys
}

// Each worker receives its own frozen dial authority. Concurrent preparations
// cannot borrow credentials or endpoints through shared mutable provider state.
type guestPinnedOpener struct{}

func (guestPinnedOpener) OpenDuplex(ctx context.Context, target domain.MachineRef, command string) (guestssh.Duplex, error) {
	snapshot, ok := ctx.Value(guestDialSnapshotKey{}).(guestDialSnapshot)
	if !ok || snapshot.target != target || snapshot.keys == nil {
		return nil, app.ErrGuestAuthority
	}
	return guestssh.NewTransport(snapshot.keys).OpenDuplex(ctx, target, command)
}

type defaultGuestTransport struct {
	client   *stream.Client
	provider *guestConfigurationProvider
}

func newDefaultGuestTransport(server *Server, keys guestssh.KeyProvider) *defaultGuestTransport {
	return &defaultGuestTransport{
		client:   stream.NewClient(guestPinnedOpener{}),
		provider: &guestConfigurationProvider{server: server, keys: keys},
	}
}

func (t *defaultGuestTransport) Prepare(ctx context.Context, target domain.MachineRef, poolKey, generation string) (stream.WorkerIdentity, error) {
	keys, err := freezeGuestKeys(ctx, t.provider.keys, target)
	if err != nil {
		return stream.WorkerIdentity{}, err
	}
	caller, err := t.prepareCaller(ctx, target, generation, keys)
	if err != nil {
		return stream.WorkerIdentity{}, err
	}
	if err = t.checkGeneration(ctx, target, generation, caller); err != nil {
		return stream.WorkerIdentity{}, err
	}
	bound := context.WithValue(ctx, guestDialSnapshotKey{}, guestDialSnapshot{target: target, keys: keys})
	identity, err := t.client.Prepare(bound, target, poolKey, generation)
	if err != nil {
		return identity, err
	}
	// Return the admitted original identity even on post-start authority drift.
	// The application must durably retire that exact worker, never replay startup.
	return identity, t.checkGeneration(ctx, target, generation, caller)
}

func (t *defaultGuestTransport) prepareCaller(ctx context.Context, target domain.MachineRef, generation string, keys *frozenGuestKeys) (domain.ActorContext, error) {
	snapshot := *t.provider
	snapshot.keys = keys
	for _, kind := range []auth.TokenType{auth.TokenTypeOperator, auth.TokenTypeAgentMCP} {
		token, err := auth.ReadTokenFile(t.provider.server.stateDir.AuthDir(), kind)
		if err != nil {
			continue
		}
		caller, _, ok := t.provider.server.authStore.Authenticate(token)
		if !ok || caller == nil || caller.IsDelegated() {
			continue
		}
		configuration, err := snapshot.Current(ctx, target, *caller)
		if err == nil && configuration.Generation == generation {
			return caller.Clone(), nil
		}
	}
	return domain.ActorContext{}, app.ErrGuestAuthority
}

func (t *defaultGuestTransport) checkGeneration(ctx context.Context, target domain.MachineRef, generation string, caller domain.ActorContext) error {
	configuration, err := t.provider.Current(ctx, target, caller)
	if err != nil || configuration.Generation != generation {
		return app.ErrGuestAuthority
	}
	return ctx.Err()
}

func (t *defaultGuestTransport) Execute(ctx context.Context, target domain.MachineRef, poolKey, generation string, request stream.Request, input io.Reader, output io.Writer) (stream.Result, error) {
	return t.client.Execute(ctx, target, poolKey, generation, request, input, output)
}

func (t *defaultGuestTransport) Retire(target domain.MachineRef, poolKey, generation string) error {
	return t.client.Retire(target, poolKey, generation)
}
