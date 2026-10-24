package ssh_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/guest/ssh"
	"github.com/Horcag/agent-machine-control/internal/guest/ssh/fakeserver"
)

func TestDuplexPinnedBinaryRoundTripAndSiblingIsolation(t *testing.T) {
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
	first, err := transport.OpenDuplex(ctx, "aaaaaaaa-aaaa-4aaa-baaa-aaaaaaaaaaaa", "synthetic-worker")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := transport.OpenDuplex(ctx, "aaaaaaaa-aaaa-4aaa-baaa-aaaaaaaaaaaa", "synthetic-worker")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	for _, channel := range []ssh.Duplex{first, second} {
		prefix := make([]byte, len("PS C:\\> "))
		if _, err = io.ReadFull(channel, prefix); err != nil {
			t.Fatal(err)
		}
	}
	_ = first.Close()
	payload := []byte{0, 255, 128, 13, 10, 1}
	if _, err = second.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(second, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("binary: %v %v", got, err)
	}
	provider.PinnedKeySHA256 = strings.Repeat("a", 44)
	if ch, err := transport.OpenDuplex(ctx, "aaaaaaaa-aaaa-4aaa-baaa-aaaaaaaaaaaa", "synthetic-worker"); err == nil {
		_ = ch.Close()
		t.Fatal("untrusted host accepted")
	}
}
func TestDuplexRejectsInvalidStartupAndCancelledDial(t *testing.T) {
	transport := ssh.NewTransport(nil)
	for _, command := range []string{"", strings.Repeat("x", 8193)} {
		if _, err := transport.OpenDuplex(context.Background(), "target", command); err == nil {
			t.Fatal("invalid command")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := transport.OpenDuplex(ctx, "target", "worker"); err == nil {
		t.Fatal("cancelled startup")
	}
}
