package ssh_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/guest/ssh"
	gossh "golang.org/x/crypto/ssh"
)

func TestDuplexExecNeverAllocatesPTY(t *testing.T) {
	clientSigner, _ := generateClientKey(t)
	hostSigner, _ := generateClientKey(t)
	config := &gossh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(hostSigner)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var pty, execRequests atomic.Int64
	var wg sync.WaitGroup
	wg.Go(func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		serveDuplexProtocol(conn, config, &pty, &execRequests)
	})
	defer func() { _ = listener.Close(); wg.Wait() }()
	hostHash := sha256.Sum256(hostSigner.PublicKey().Marshal())
	provider := &ssh.MockKeyProvider{Signer: clientSigner, PinnedKeySHA256: base64.StdEncoding.EncodeToString(hostHash[:]), Endpoint: listener.Addr().String(), User: "synthetic-user"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ch, err := ssh.NewTransport(provider).OpenDuplex(ctx, "target", "fixed-startup")
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	payload := []byte{0, 255, 128, 13, 10}
	_, err = ch.Write(payload)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(ch, got); err != nil || !bytes.Equal(payload, got) {
		t.Fatal(got, err)
	}
	if pty.Load() != 0 || execRequests.Load() != 1 {
		t.Fatalf("PTY=%d exec=%d", pty.Load(), execRequests.Load())
	}
}

func serveDuplexProtocol(conn net.Conn, config *gossh.ServerConfig, pty, execRequests *atomic.Int64) {
	server, channels, requests, err := gossh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer server.Close()
	go gossh.DiscardRequests(requests)
	for newChannel := range channels {
		channel, requests, err := newChannel.Accept()
		if err != nil {
			return
		}
		for request := range requests {
			switch request.Type {
			case "pty-req":
				pty.Add(1)
				_ = request.Reply(false, nil)
			case "exec":
				execRequests.Add(1)
				_ = request.Reply(true, nil)
				_, _ = io.Copy(channel, channel)
				_ = channel.Close()
				return
			default:
				_ = request.Reply(false, nil)
			}
		}
	}
}
