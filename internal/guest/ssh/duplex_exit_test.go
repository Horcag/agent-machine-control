package ssh_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/guest/ssh"
	"github.com/Horcag/agent-machine-control/internal/guest/ssh/fakeserver"
)

func TestDuplexExitStatusCachedAndContextBounded(t *testing.T) {
	signer, key := generateClientKey(t)
	server, err := fakeserver.New(fakeserver.ModeExitEarly, key)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	server.SetExitCode(214)
	provider := &ssh.MockKeyProvider{Signer: signer, PinnedKeySHA256: server.HostKeyPin(), Endpoint: server.Addr(), User: "synthetic"}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	channel, err := ssh.NewTransport(provider).OpenDuplex(ctx, "target", "fixed-worker")
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	if _, err = io.Copy(io.Discard, channel); err != nil {
		t.Fatal(err)
	}
	observer := channel.(interface {
		ExitStatus(context.Context) (int, bool)
	})
	for range 2 {
		if code, known := observer.ExitStatus(ctx); !known || code != 214 {
			t.Fatal("exit not cached", code, known)
		}
		if channel.Wait() == nil {
			t.Fatal("nonzero exit lost")
		}
	}
	cancelled, stop := context.WithCancel(t.Context())
	stop()
	if _, known := observer.ExitStatus(cancelled); known {
		t.Fatal("cancelled observation adopted exit")
	}
}
