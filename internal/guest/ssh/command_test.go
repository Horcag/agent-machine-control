package ssh_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/guest/ssh"
	"github.com/Horcag/agent-machine-control/internal/guest/ssh/fakeserver"
)

func TestSSHCommandUsesPinnedCredentialsAndStdin(t *testing.T) {
	signer, key := generateClientKey(t)
	server, err := fakeserver.New(fakeserver.ModeEcho, key)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	provider := &ssh.MockKeyProvider{Signer: signer, PinnedKeySHA256: server.HostKeyPin(), Endpoint: server.Addr(), User: "synthetic-user"}
	transport := ssh.NewTransport(provider)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	output, err := transport.RunCommand(ctx, "aaaaaaaa-aaaa-4aaa-baaa-aaaaaaaaaaaa", "synthetic-command", []byte("synthetic input"), 4096)
	if err != nil || !strings.Contains(string(output), "synthetic input") {
		t.Fatalf("stdin/output: %q %v", output, err)
	}
	provider.PinnedKeySHA256 = strings.Repeat("a", 44)
	if _, err := transport.RunCommand(ctx, "aaaaaaaa-aaaa-4aaa-baaa-aaaaaaaaaaaa", "synthetic-command", nil, 4096); err == nil {
		t.Fatal("untrusted host executed command")
	}
}

func TestSSHCommandRejectsFailureAndBoundsOutput(t *testing.T) {
	cases := []struct {
		mode    fakeserver.Mode
		timeout time.Duration
	}{
		{fakeserver.ModeFlood, 2 * time.Second},
		{fakeserver.ModeExitEarly, 2 * time.Second},
		// Cancel well before the fixture can finish its two-second stall normally.
		{fakeserver.ModeStallInput, 500 * time.Millisecond},
	}
	for _, test := range cases {
		mode := test.mode
		t.Run(string(mode), func(t *testing.T) {
			signer, key := generateClientKey(t)
			server, err := fakeserver.New(mode, key)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			provider := &ssh.MockKeyProvider{Signer: signer, PinnedKeySHA256: server.HostKeyPin(), Endpoint: server.Addr(), User: "synthetic-user"}
			if mode == fakeserver.ModeExitEarly {
				server.SetExitCode(1)
			}
			ctx, cancel := context.WithTimeout(context.Background(), test.timeout)
			defer cancel()
			output, err := ssh.NewTransport(provider).RunCommand(ctx, "aaaaaaaa-aaaa-4aaa-baaa-aaaaaaaaaaaa", "synthetic-command", nil, 1024)
			if err == nil || output != nil {
				t.Fatal("unsafe command result accepted")
			}
			if mode == fakeserver.ModeFlood && !errors.Is(err, ssh.ErrCommandOutputLimit) {
				t.Fatalf("output limit: %v", err)
			}
			if mode == fakeserver.ModeStallInput && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("deadline: %v", err)
			}
		})
	}
}

func TestSSHCommandRejectsInvalidBoundsBeforeCredentialResolution(t *testing.T) {
	transport := ssh.NewTransport(nil)
	for _, limit := range []int{0, 1024*1024 + 1} {
		if _, err := transport.RunCommand(context.Background(), "synthetic", "command", nil, limit); !errors.Is(err, ssh.ErrCommandFailed) {
			t.Fatalf("invalid bounds: %v", err)
		}
	}
}
