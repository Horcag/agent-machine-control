package fakeserver

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

func TestFakeSSHServer_ModesAndExitCode(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("failed to create signer: %v", err)
	}
	sshPub, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("failed to create ssh pubkey: %v", err)
	}

	modes := []Mode{ModeEcho, ModeStallInput, ModeExitEarly, ModeOSC52, ModeSplitRune, ModeFlood}

	for _, mode := range modes {
		srv, err := New(mode, sshPub)
		if err != nil {
			t.Fatalf("failed to create fake server for mode %s: %v", mode, err)
		}
		if srv.Addr() == "" {
			t.Errorf("expected non-empty addr")
		}
		if srv.HostKeyPin() == "" {
			t.Errorf("expected non-empty host key pin")
		}
		srv.SetExitCode(42)

		// Connect client
		clientConfig := &gossh.ClientConfig{
			User:            "testuser",
			Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
			HostKeyCallback: gossh.FixedHostKey(srv.hostSigner.PublicKey()),
			Timeout:         2 * time.Second,
		}

		client, err := gossh.Dial("tcp", srv.Addr(), clientConfig)
		if err != nil {
			_ = srv.Close()
			t.Fatalf("failed to dial mode %s: %v", mode, err)
		}

		sess, err := client.NewSession()
		if err != nil {
			_ = client.Close()
			_ = srv.Close()
			t.Fatalf("failed to create session for mode %s: %v", mode, err)
		}

		_ = sess.RequestPty("xterm", 24, 80, gossh.TerminalModes{})
		_ = sess.WindowChange(30, 100)
		_ = sess.Shell()

		if mode == ModeEcho {
			stdin, _ := sess.StdinPipe()
			if stdin != nil {
				_, _ = stdin.Write([]byte("dir\r\n"))
				time.Sleep(20 * time.Millisecond)
				_, _ = stdin.Write([]byte("\x03"))
				time.Sleep(20 * time.Millisecond)
				_, _ = stdin.Write([]byte("exit\r\n"))
			}
		}

		time.Sleep(50 * time.Millisecond)
		_ = sess.Close()
		_ = client.Close()
		_ = srv.Close()
	}
}

func TestFakeSSHServer_ExecAndUnknownChannel(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := gossh.NewSignerFromKey(priv)
	sshPub, _ := gossh.NewPublicKey(pub)

	srv, err := New(ModeEcho, sshPub)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	defer srv.Close()

	clientConfig := &gossh.ClientConfig{
		User:            "testuser",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: gossh.FixedHostKey(srv.hostSigner.PublicKey()),
		Timeout:         2 * time.Second,
	}

	client, err := gossh.Dial("tcp", srv.Addr(), clientConfig)
	if err != nil {
		t.Fatalf("failed to dial: %v", err)
	}
	defer client.Close()

	// 1. Exec request
	sess, err := client.NewSession()
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	_ = sess.Run("whoami")
	_ = sess.Close()

	// 2. Reject unknown channel
	_, _, err = client.OpenChannel("direct-tcpip", nil)
	if err == nil {
		t.Errorf("expected rejection of non-session channel")
	}
}

func TestFakeSSHServer_UnauthorizedKeyRejection(t *testing.T) {
	pub1, _, _ := ed25519.GenerateKey(rand.Reader)
	sshPub1, _ := gossh.NewPublicKey(pub1)

	_, priv2, _ := ed25519.GenerateKey(rand.Reader)
	signer2, _ := gossh.NewSignerFromKey(priv2)

	srv, err := New(ModeEcho, sshPub1)
	if err != nil {
		t.Fatalf("failed to create fake server: %v", err)
	}
	defer srv.Close()

	clientConfig := &gossh.ClientConfig{
		User:            "testuser",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer2)},
		HostKeyCallback: gossh.FixedHostKey(srv.hostSigner.PublicKey()),
		Timeout:         1 * time.Second,
	}

	client, err := gossh.Dial("tcp", srv.Addr(), clientConfig)
	if err == nil {
		_ = client.Close()
		t.Errorf("expected handshake error for unauthorized public key")
	}
}
