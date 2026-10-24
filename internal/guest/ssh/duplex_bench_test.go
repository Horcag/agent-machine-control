package ssh_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/guest/ssh"
	gossh "golang.org/x/crypto/ssh"
)

const duplexBenchmarkPayloadSize = 64 << 20
const duplexBenchmarkChunkSize = 64 << 10

// BenchmarkDuplexStreaming64MiB compares binary SSH channel throughput over
// IPv4 and IPv6 loopback using the normal x/crypto/ssh algorithm preferences.
// The server echoes concurrently so neither channel window can deadlock.
func BenchmarkDuplexStreaming64MiB(b *testing.B) {
	for _, tc := range []struct {
		name, listenAddress string
	}{
		{name: "IPv4", listenAddress: "127.0.0.1:0"},
		{name: "IPv6", listenAddress: "[::1]:0"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			benchmarkDuplexStreaming(b, tc.listenAddress)
		})
	}
}

func benchmarkDuplexStreaming(b *testing.B, listenAddress string) {

	clientSigner := newDuplexBenchmarkSigner(b)
	hostSigner := newDuplexBenchmarkSigner(b)
	serverConfig := &gossh.ServerConfig{NoClientAuth: true}
	serverConfig.AddHostKey(hostSigner)
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		b.Fatal(err)
	}
	var serverWG sync.WaitGroup
	negotiated := make(chan gossh.NegotiatedAlgorithms, 1)
	serverWG.Go(func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			serverWG.Go(func() { serveDuplexBenchmarkConn(conn, serverConfig, negotiated) })
		}
	})
	defer func() {
		_ = listener.Close()
		serverWG.Wait()
	}()

	hostHash := sha256.Sum256(hostSigner.PublicKey().Marshal())
	provider := &ssh.MockKeyProvider{
		Signer:          clientSigner,
		PinnedKeySHA256: base64.StdEncoding.EncodeToString(hostHash[:]),
		Endpoint:        listener.Addr().String(),
		User:            "synthetic-benchmark-user",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	duplex, err := ssh.NewTransport(provider).OpenDuplex(ctx, "synthetic-benchmark-target", "synthetic-benchmark-worker")
	if err != nil {
		cancel()
		b.Fatal(err)
	}
	stopClose := context.AfterFunc(ctx, func() { _ = duplex.Close() })
	defer func() {
		stopClose()
		cancel()
		_ = duplex.Close()
	}()
	algorithms := <-negotiated
	b.Logf("negotiated SSH write cipher: %s", algorithms.Write.Cipher)

	payload := make([]byte, duplexBenchmarkPayloadSize)
	for i := range payload {
		payload[i] = byte(i*31 + i/251)
	}
	writeDone := make(chan error, 1)
	reader := make([]byte, duplexBenchmarkChunkSize)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		go func() { writeDone <- writeDuplexBenchmarkPayload(duplex, payload) }()
		if err = verifyDuplexBenchmarkPayload(duplex, payload, reader); err != nil {
			b.Fatal(err)
		}

		if err = <-writeDone; err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(duplexBenchmarkPayloadSize*b.N)/b.Elapsed().Seconds()/(1<<20), "MiB/s")
}

func serveDuplexBenchmarkConn(conn net.Conn, config *gossh.ServerConfig, negotiated chan<- gossh.NegotiatedAlgorithms) {
	server, channels, requests, err := gossh.NewServerConn(conn, config)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer server.Close()
	negotiated <- server.Conn.(gossh.AlgorithmsConnMetadata).Algorithms()
	go gossh.DiscardRequests(requests)
	var sessionWG sync.WaitGroup
	for newChannel := range channels {
		channel, requests, acceptErr := newChannel.Accept()
		if acceptErr != nil {
			return
		}
		sessionWG.Go(func() {
			defer channel.Close()
			for request := range requests {
				if request.Type == "exec" {
					_ = request.Reply(true, nil)
					_, _ = io.Copy(channel, channel)
					return
				}
				_ = request.Reply(false, nil)
			}
		})
	}
	sessionWG.Wait()
}

func newDuplexBenchmarkSigner(b *testing.B) gossh.Signer {
	b.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(privateKey)
	if err != nil {
		b.Fatal(err)
	}
	return signer
}

func writeDuplexBenchmarkPayload(writer io.Writer, payload []byte) error {
	for offset := 0; offset < len(payload); {
		end := min(offset+duplexBenchmarkChunkSize, len(payload))
		chunk := payload[offset:end]
		for len(chunk) > 0 {
			n, err := writer.Write(chunk)
			if err != nil {
				return err
			}
			if n == 0 {
				return io.ErrShortWrite
			}
			chunk = chunk[n:]
		}
		offset = end
	}
	return nil
}

func verifyDuplexBenchmarkPayload(reader io.Reader, payload, buffer []byte) error {
	for offset := 0; offset < len(payload); {
		end := min(offset+len(buffer), len(payload))
		if _, err := io.ReadFull(reader, buffer[:end-offset]); err != nil {
			return err
		}
		if !bytes.Equal(buffer[:end-offset], payload[offset:end]) {
			return fmt.Errorf("echo differs at byte %d", offset)
		}
		offset = end
	}
	return nil
}
