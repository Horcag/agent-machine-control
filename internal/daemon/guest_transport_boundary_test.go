package daemon

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
)

func guestPreparationGenerations(t *testing.T, provider guestConfigurationProvider, target domain.MachineRef) []string {
	t.Helper()
	var generations []string
	for _, kind := range []auth.TokenType{auth.TokenTypeOperator, auth.TokenTypeAgentMCP} {
		token, err := auth.ReadTokenFile(provider.server.stateDir.AuthDir(), kind)
		if err != nil {
			t.Fatal(err)
		}
		caller, _, ok := provider.server.authStore.Authenticate(token)
		if !ok {
			t.Fatal("fixture caller unavailable")
		}
		configuration, err := provider.Current(t.Context(), target, *caller)
		if err != nil {
			t.Fatal(err)
		}
		generations = append(generations, configuration.Generation)
	}
	return generations
}

func TestGuestDefaultPrepareConcurrentActorIsolation(t *testing.T) {
	provider, _, _, _ := newGuestConfigurationFixture(t)
	target := domain.MachineRef("local:11111111-1111-4111-8111-111111111111")
	generations := guestPreparationGenerations(t, provider, target)
	if generations[0] == generations[1] {
		t.Fatal("actors shared an authority generation")
	}
	transport, calls, original := guestPreparedTestTransport(t, provider, nil)
	results := make(chan error, len(generations))
	var workers sync.WaitGroup
	for _, generation := range generations {
		workers.Go(func() {
			identity, err := transport.Prepare(t.Context(), target, "same-opaque-pool-key", generation)
			if err == nil && identity != original {
				err = errors.New("original worker identity changed")
			}
			results <- err
		})
	}
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("actor generations borrowed a worker: starts=%d", calls.Load())
	}
}

func TestGuestDefaultPrepareCancellationReachesStartup(t *testing.T) {
	provider, _, _, _ := newGuestConfigurationFixture(t)
	target := domain.MachineRef("local:11111111-1111-4111-8111-111111111111")
	generation := guestPreparationGenerations(t, provider, target)[0]
	entered := make(chan struct{})
	client := stream.NewClient(guestPrepareTestOpener(func(ctx context.Context, _ domain.MachineRef, _ string) (guestssh.Duplex, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	t.Cleanup(func() { _ = client.Close() })
	transport := defaultGuestTransport{client: client, provider: &provider}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := transport.Prepare(ctx, target, "canceled-original-startup", generation)
		result <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("startup did not reach its original context")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("startup cancellation lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("startup ignored caller cancellation")
	}
}

func TestGuestPinnedOpenerUsesOriginalFrozenEndpoint(t *testing.T) {
	provider, keys, _, config := newGuestConfigurationFixture(t)
	target := domain.MachineRef("local:11111111-1111-4111-8111-111111111111")
	frozen, err := freezeGuestKeys(t.Context(), provider.keys, target)
	if err != nil {
		t.Fatal(err)
	}
	keys.endpoint = "127.0.0.1:65001"
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, guestDialSnapshotKey{}, guestDialSnapshot{target: target, keys: frozen})
	channel, err := (guestPinnedOpener{}).OpenDuplex(ctx, target, "synthetic-observation")
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	prefix := make([]byte, len("PS C:\\> "))
	if _, err = io.ReadFull(channel, prefix); err != nil || string(prefix) != "PS C:\\> " {
		t.Fatalf("original pinned route not observed: %q %v", prefix, err)
	}
	endpoint, _ := frozen.GetGuestEndpoint(ctx, target)
	if endpoint != config.Endpoint {
		t.Fatal("frozen endpoint changed")
	}
}
