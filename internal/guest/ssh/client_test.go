package ssh_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/guest/ssh"
	"github.com/Horcag/agent-machine-control/internal/guest/ssh/fakeserver"
	gossh "golang.org/x/crypto/ssh"
)

func generateClientKey(t *testing.T) (gossh.Signer, gossh.PublicKey) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate ed25519 key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("failed to create signer: %v", err)
	}
	sshPub, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("failed to create ssh pubkey: %v", err)
	}
	return signer, sshPub
}

func TestSSHTransport_FullSessionLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	signer, clientPub := generateClientKey(t)
	server, err := fakeserver.New(fakeserver.ModeEcho, clientPub)
	if err != nil {
		t.Fatalf("failed to create fake ssh server: %v", err)
	}
	defer server.Close()

	kp := &ssh.MockKeyProvider{
		Signer:          signer,
		PinnedKeySHA256: server.HostKeyPin(),
		Endpoint:        server.Addr(),
		User:            "testadmin",
	}

	transport := ssh.NewTransport(kp)
	channel, err := transport.Dial(ctx, "c4a523d4-6b99-4d62-a5e2-4752c0f20001", 80, 24, "xterm-256color")
	if err != nil {
		t.Fatalf("transport.Dial failed: %v", err)
	}
	defer func() { _ = channel.Close(context.Background()) }()

	// Read initial prompt
	buf := make([]byte, 1024)
	n, err := channel.Read(buf)
	if err != nil {
		t.Fatalf("channel.Read failed: %v", err)
	}
	if !strings.Contains(string(buf[:n]), "PS C:\\>") {
		t.Errorf("expected initial prompt, got: %q", string(buf[:n]))
	}

	// Write command
	cmd := "dir\r\n"
	wn, err := channel.Write(ctx, []byte(cmd))
	if err != nil || wn != len(cmd) {
		t.Fatalf("channel.Write got (%d, %v), want (%d, nil)", wn, err, len(cmd))
	}

	// Read echo response
	n, err = channel.Read(buf)
	if err != nil {
		t.Fatalf("read after write failed: %v", err)
	}
	if !strings.Contains(string(buf[:n]), "dir") {
		t.Errorf("expected echo 'dir', got: %q", string(buf[:n]))
	}

	// Send Control Key (Ctrl+C)
	if _, err := channel.SendControl(ctx, domain.ControlKeyCtrlC); err != nil {
		t.Fatalf("SendControl failed: %v", err)
	}

	// Window resize
	if err := channel.Resize(120, 40); err != nil {
		t.Fatalf("Resize failed: %v", err)
	}

	// Close session
	if err := channel.Close(ctx); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Subsequent write fails with ErrSessionClosed
	_, err = channel.Write(ctx, []byte("more"))
	if err != domain.ErrSessionClosed {
		t.Errorf("expected ErrSessionClosed, got: %v", err)
	}
}

func TestSSHTransport_HostKeyMismatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	signer, clientPub := generateClientKey(t)
	server, err := fakeserver.New(fakeserver.ModeEcho, clientPub)
	if err != nil {
		t.Fatalf("failed to create fake ssh server: %v", err)
	}
	defer server.Close()

	kp := &ssh.MockKeyProvider{
		Signer:          signer,
		PinnedKeySHA256: strings.Repeat("a", 44),
		Endpoint:        server.Addr(),
		FailPinMismatch: true,
	}

	transport := ssh.NewTransport(kp)
	_, err = transport.Dial(ctx, "c4a523d4-6b99-4d62-a5e2-4752c0f20001", 80, 24, "xterm-256color")
	if err == nil {
		t.Fatal("expected Dial to fail on host key mismatch")
	}
}

func TestSSHTransport_MissingPin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	signer, clientPub := generateClientKey(t)
	server, err := fakeserver.New(fakeserver.ModeEcho, clientPub)
	if err != nil {
		t.Fatalf("failed to create fake ssh server: %v", err)
	}
	defer server.Close()

	kp := &ssh.MockKeyProvider{
		Signer:         signer,
		Endpoint:       server.Addr(),
		FailPinMissing: true,
	}

	transport := ssh.NewTransport(kp)
	_, err = transport.Dial(ctx, "c4a523d4-6b99-4d62-a5e2-4752c0f20001", 80, 24, "xterm-256color")
	if err == nil {
		t.Fatal("expected Dial to fail on missing host key pin")
	}
}

func TestSSHTransport_UnauthorizedClientKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	signer, _ := generateClientKey(t)
	_, otherPub := generateClientKey(t)

	server, err := fakeserver.New(fakeserver.ModeEcho, otherPub)
	if err != nil {
		t.Fatalf("failed to create fake ssh server: %v", err)
	}
	defer server.Close()

	kp := &ssh.MockKeyProvider{
		Signer:          signer,
		PinnedKeySHA256: server.HostKeyPin(),
		Endpoint:        server.Addr(),
	}

	transport := ssh.NewTransport(kp)
	_, err = transport.Dial(ctx, "c4a523d4-6b99-4d62-a5e2-4752c0f20001", 80, 24, "xterm-256color")
	if err == nil {
		t.Fatal("expected Dial to fail on unauthorized public key")
	}
}

func TestSSHTransport_InvalidDimensionsAndNilProvider(t *testing.T) {
	ctx := context.Background()

	// Nil key provider
	nilTransport := ssh.NewTransport(nil)
	_, err := nilTransport.Dial(ctx, "c4a523d4-6b99-4d62-a5e2-4752c0f20001", 80, 24, "xterm-256color")
	if err == nil {
		t.Errorf("expected error on nil key provider")
	}

	// Invalid dimensions
	signer, clientPub := generateClientKey(t)
	server, _ := fakeserver.New(fakeserver.ModeEcho, clientPub)
	defer server.Close()

	kp := &ssh.MockKeyProvider{
		Signer:          signer,
		PinnedKeySHA256: server.HostKeyPin(),
		Endpoint:        server.Addr(),
		User:            "testadmin",
	}
	transport := ssh.NewTransport(kp)

	_, err = transport.Dial(ctx, "c4a523d4-6b99-4d62-a5e2-4752c0f20001", 5, 24, "xterm-256color")
	if err == nil {
		t.Errorf("expected error on cols < 20")
	}

	_, err = transport.Dial(ctx, "c4a523d4-6b99-4d62-a5e2-4752c0f20001", 80, 2, "xterm-256color")
	if err == nil {
		t.Errorf("expected error on rows < 5")
	}
}

func TestSSHTransport_ChannelMethods(t *testing.T) {
	ctx := context.Background()
	signer, clientPub := generateClientKey(t)
	server, _ := fakeserver.New(fakeserver.ModeExitEarly, clientPub)
	defer server.Close()

	kp := &ssh.MockKeyProvider{
		Signer:          signer,
		PinnedKeySHA256: server.HostKeyPin(),
		Endpoint:        server.Addr(),
		User:            "testadmin",
	}
	transport := ssh.NewTransport(kp)

	channel, err := transport.Dial(ctx, "c4a523d4-6b99-4d62-a5e2-4752c0f20001", 80, 24, "xterm-256color")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}

	// Invalid control key
	if _, err := channel.SendControl(ctx, "invalid-control-key"); err == nil {
		t.Errorf("expected error on invalid control key")
	}

	// Close channel
	firstCloseErr := channel.Close(ctx)

	// Resize after close
	if err := channel.Resize(80, 24); err != domain.ErrSessionClosed {
		t.Errorf("expected ErrSessionClosed on Resize after close, got %v", err)
	}

	// Close again idempotent
	if err := channel.Close(ctx); !errors.Is(err, firstCloseErr) {
		t.Errorf("idempotent Close error = %v, want cached %v", err, firstCloseErr)
	}

	// Wait
	code, _ := channel.Wait()
	if code != 0 {
		t.Errorf("expected 0 exit code on early exit, got %d", code)
	}
}

func TestSSHTransport_DialProviderErrors(t *testing.T) {
	ctx := context.Background()
	signer, _ := generateClientKey(t)

	// Missing host key pin
	kpMissingPin := &ssh.MockKeyProvider{
		Signer:         signer,
		FailPinMissing: true,
	}
	tMissing := ssh.NewTransport(kpMissingPin)
	if _, err := tMissing.Dial(ctx, "c4a523d4-6b99-4d62-a5e2-4752c0f20001", 80, 24, "xterm-256color"); err == nil {
		t.Errorf("expected error on missing host key pin")
	}

	// Signer nil error
	kpNilSigner := &ssh.MockKeyProvider{
		PinnedKeySHA256: strings.Repeat("a", 44),
	}
	tNilSigner := ssh.NewTransport(kpNilSigner)
	if _, err := tNilSigner.Dial(ctx, "c4a523d4-6b99-4d62-a5e2-4752c0f20001", 80, 24, "xterm-256color"); err == nil {
		t.Errorf("expected error on nil client signer")
	}

	// TCP dial error on unreachable endpoint
	kpUnreachable := &ssh.MockKeyProvider{
		Signer:          signer,
		PinnedKeySHA256: strings.Repeat("a", 44),
		Endpoint:        "127.0.0.1:1", // closed port
	}
	tUnreachable := ssh.NewTransport(kpUnreachable)
	if _, err := tUnreachable.Dial(ctx, "c4a523d4-6b99-4d62-a5e2-4752c0f20001", 80, 24, "xterm-256color"); err == nil {
		t.Errorf("expected error on unreachable endpoint")
	}
}

func TestSSHTransport_NonZeroExitStatus(t *testing.T) {
	signer, _ := generateClientKey(t)
	server, err := fakeserver.New(fakeserver.ModeExitEarly, signer.PublicKey())
	if err != nil {
		t.Fatalf("fakeserver.New failed: %v", err)
	}
	server.SetExitCode(42)
	defer server.Close()

	kp := &ssh.MockKeyProvider{
		Signer:          signer,
		PinnedKeySHA256: server.HostKeyPin(),
		Endpoint:        server.Addr(),
		User:            "testadmin",
	}
	transport := ssh.NewTransport(kp)
	ctx := context.Background()

	channel, err := transport.Dial(ctx, "c4a523d4-6b99-4d62-a5e2-4752c0f20001", 80, 24, "xterm-256color")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}

	code, _ := channel.Wait()
	if code != 42 {
		t.Errorf("expected exit code 42, got %d", code)
	}
}

func TestSSHTransport_WriteCancellationAndDialConfigErrors(t *testing.T) {
	signer, clientPub := generateClientKey(t)
	server, err := fakeserver.New(fakeserver.ModeStallInput, clientPub)
	if err != nil {
		t.Fatalf("fakeserver.New failed: %v", err)
	}
	defer server.Close()

	kp := &ssh.MockKeyProvider{
		Signer:          signer,
		PinnedKeySHA256: server.HostKeyPin(),
		Endpoint:        server.Addr(),
		User:            "testadmin",
	}
	transport := ssh.NewTransport(kp)
	ctx := context.Background()

	channel, err := transport.Dial(ctx, "c4a523d4-6b99-4d62-a5e2-4752c0f20001", 80, 24, "xterm-256color")
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer func() { _ = channel.Close(context.Background()) }()

	// Write with already cancelled context
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := channel.Write(canceledCtx, []byte("quick")); err == nil {
		t.Errorf("expected error on write with canceled context")
	}

	// Mock key provider with explicit machine config
	cfgKP := &ssh.MockKeyProvider{
		MachineConfig: &ssh.MachineSSHConfig{
			Endpoint: "10.10.10.10:22",
			User:     "explicit_admin",
		},
	}
	cfg, err := cfgKP.GetMachineConfig("c4a523d4-6b99-4d62-a5e2-4752c0f20001")
	if err != nil || cfg.User != "explicit_admin" {
		t.Errorf("expected explicit machine config, got %+v (err %v)", cfg, err)
	}
}
