package daemon

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
)

type guestPrepareTestOpener func(context.Context, domain.MachineRef, string) (guestssh.Duplex, error)

func (f guestPrepareTestOpener) OpenDuplex(ctx context.Context, target domain.MachineRef, command string) (guestssh.Duplex, error) {
	return f(ctx, target, command)
}

type guestPrepareTestChannel struct{ net.Conn }

func (guestPrepareTestChannel) Wait() error { return nil }

func guestPreparedTestTransport(t *testing.T, provider guestConfigurationProvider, afterStartup func()) (*defaultGuestTransport, *atomic.Int32, stream.WorkerIdentity) {
	t.Helper()
	identity := stream.WorkerIdentity{PID: 1234, BirthTicks: 638000000000000001, Image: "synthetic-original-worker", SID: "synthetic-owner"}
	calls := new(atomic.Int32)
	var workers sync.WaitGroup
	client := stream.NewClient(guestPrepareTestOpener(func(ctx context.Context, target domain.MachineRef, command string) (guestssh.Duplex, error) {
		snapshot, ok := ctx.Value(guestDialSnapshotKey{}).(guestDialSnapshot)
		if !ok || snapshot.target != target || snapshot.keys == nil || command != stream.StartupCommand {
			return nil, errors.New("startup lost its frozen authority")
		}
		calls.Add(1)
		local, remote := net.Pipe()
		workers.Go(func() {
			defer remote.Close()
			var length uint32
			if binary.Read(remote, binary.BigEndian, &length) != nil || length > stream.MaxControl {
				return
			}
			if _, err := io.CopyN(io.Discard, remote, int64(length)); err != nil {
				return
			}
			if afterStartup != nil {
				afterStartup()
			}
			data, _ := json.Marshal(identity)
			// #nosec G115 -- fixed synthetic identity JSON is below the control bound.
			if binary.Write(remote, binary.BigEndian, uint32(len(data))) != nil {
				return
			}
			if _, err := remote.Write(data); err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, remote)
		})
		return guestPrepareTestChannel{local}, nil
	}))
	t.Cleanup(func() { _ = client.Close(); workers.Wait() })
	return &defaultGuestTransport{client: client, provider: &provider}, calls, identity
}

func TestGuestDefaultPrepareMatchesAuthenticatedActorGeneration(t *testing.T) {
	for _, kind := range []auth.TokenType{auth.TokenTypeOperator, auth.TokenTypeAgentMCP} {
		t.Run(string(kind), func(t *testing.T) {
			provider, _, _, _ := newGuestConfigurationFixture(t)
			token, err := auth.ReadTokenFile(provider.server.stateDir.AuthDir(), kind)
			if err != nil {
				t.Fatal(err)
			}
			caller, _, ok := provider.server.authStore.Authenticate(token)
			if !ok {
				t.Fatal("fixture token not authenticated")
			}
			target := domain.MachineRef("local:11111111-1111-4111-8111-111111111111")
			configuration, err := provider.Current(t.Context(), target, *caller)
			if err != nil {
				t.Fatal(err)
			}
			transport, calls, original := guestPreparedTestTransport(t, provider, nil)
			actual, err := transport.Prepare(t.Context(), target, "opaque-pool-key", configuration.Generation)
			if err != nil || actual != original || calls.Load() != 1 {
				t.Fatalf("actor generation lost: %+v %v calls=%d", actual, err, calls.Load())
			}
		})
	}
}

func TestGuestDefaultPreparePreservesOriginalIdentityAfterDrift(t *testing.T) {
	provider, keys, caller, _ := newGuestConfigurationFixture(t)
	target := domain.MachineRef("local:11111111-1111-4111-8111-111111111111")
	configuration, err := provider.Current(t.Context(), target, *caller)
	if err != nil {
		t.Fatal(err)
	}
	transport, calls, original := guestPreparedTestTransport(t, provider, func() { keys.endpoint = "127.0.0.1:65001" })
	actual, err := transport.Prepare(t.Context(), target, "original-pool-key", configuration.Generation)
	if !errors.Is(err, app.ErrGuestAuthority) || actual != original || calls.Load() != 1 {
		t.Fatalf("post-start drift hid original identity or replayed: %+v %v calls=%d", actual, err, calls.Load())
	}
	if err = transport.Retire(target, "original-pool-key", configuration.Generation); err != nil {
		t.Fatal(err)
	}
	if _, err = transport.Prepare(t.Context(), target, "original-pool-key", configuration.Generation); !errors.Is(err, app.ErrGuestAuthority) || calls.Load() != 1 {
		t.Fatalf("stale generation dialed again: %v calls=%d", err, calls.Load())
	}
}

func TestGuestPinnedOpenerRejectsMissingOrForeignSnapshot(t *testing.T) {
	target := domain.MachineRef("local:11111111-1111-4111-8111-111111111111")
	contexts := []context.Context{t.Context(), context.WithValue(t.Context(), guestDialSnapshotKey{}, guestDialSnapshot{target: target}), context.WithValue(t.Context(), guestDialSnapshotKey{}, guestDialSnapshot{target: "local:22222222-2222-4222-8222-222222222222", keys: &frozenGuestKeys{}})}
	for _, ctx := range contexts {
		if _, err := (guestPinnedOpener{}).OpenDuplex(ctx, target, "synthetic-observation"); !errors.Is(err, app.ErrGuestAuthority) {
			t.Fatalf("unbound route reached SSH: %v", err)
		}
	}
}
