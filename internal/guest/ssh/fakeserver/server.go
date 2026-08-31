package fakeserver

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// Mode configures fake server behavior.
type Mode string

const (
	ModeEcho       Mode = "echo"
	ModeStallInput Mode = "stall_input"
	ModeExitEarly  Mode = "exit_early"
	ModeOSC52      Mode = "osc52"
	ModeSplitRune  Mode = "split_rune"
	ModeFlood      Mode = "flood"
)

// FakeSSHServer is an in-process RFC 4254 SSH server for testing.
type FakeSSHServer struct {
	listener     net.Listener
	config       *gossh.ServerConfig
	hostSigner   gossh.Signer
	hostKeyPin   string
	mode         Mode
	exitCode     uint32
	mu           sync.Mutex
	closed       bool
	clientPublic gossh.PublicKey
}

// New creates and starts a FakeSSHServer on loopback.
func New(mode Mode, authorizedKey gossh.PublicKey) (*FakeSSHServer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate ed25519 key: %w", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		return nil, fmt.Errorf("failed to create signer: %w", err)
	}

	sum := sha256.Sum256(signer.PublicKey().Marshal())
	pin := base64.StdEncoding.EncodeToString(sum[:])

	serverConfig := &gossh.ServerConfig{
		PublicKeyCallback: func(_ gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
			if authorizedKey != nil {
				if string(key.Marshal()) != string(authorizedKey.Marshal()) {
					return nil, errors.New("unauthorized public key")
				}
			}
			return nil, nil
		},
	}
	serverConfig.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("failed to listen: %w", err)
	}

	s := &FakeSSHServer{
		listener:     ln,
		config:       serverConfig,
		hostSigner:   signer,
		hostKeyPin:   pin,
		mode:         mode,
		clientPublic: authorizedKey,
	}

	go s.serve()
	return s, nil
}

// Addr returns the loopback listener address (127.0.0.1:port).
func (s *FakeSSHServer) Addr() string {
	return s.listener.Addr().String()
}

// HostKeyPin returns the base64 SHA256 of the server's public key.
func (s *FakeSSHServer) HostKeyPin() string {
	return s.hostKeyPin
}

// SetExitCode configures the exit status returned on session termination.
func (s *FakeSSHServer) SetExitCode(code uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exitCode = code
}

// Close terminates the listener and active connections.
func (s *FakeSSHServer) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return s.listener.Close()
}

func (s *FakeSSHServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn)
	}
}

func (s *FakeSSHServer) handleConn(conn net.Conn) {
	sshConn, chans, reqs, err := gossh.NewServerConn(conn, s.config)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer sshConn.Close()

	go gossh.DiscardRequests(reqs)

	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(gossh.UnknownChannelType, "unknown channel type")
			continue
		}

		channel, requests, err := newChan.Accept()
		if err != nil {
			return
		}

		go s.handleSession(channel, requests)
	}
}

func (s *FakeSSHServer) handleSession(channel gossh.Channel, requests <-chan *gossh.Request) {
	defer channel.Close()

	for req := range requests {
		switch req.Type {
		case "pty-req":
			_ = req.Reply(true, nil)
		case "window-change":
			_ = req.Reply(true, nil)
		case "shell":
			_ = req.Reply(true, nil)
			s.runMode(channel)
			s.sendExitStatus(channel)
			return
		case "exec":
			_ = req.Reply(true, nil)
			s.runMode(channel)
			s.sendExitStatus(channel)
			return
		default:
			_ = req.Reply(false, nil)
		}
	}
}

func (s *FakeSSHServer) runEchoMode(channel gossh.Channel) {
	_, _ = channel.Write([]byte("PS C:\\> "))
	buf := make([]byte, 1024)
	for {
		n, err := channel.Read(buf)
		if n > 0 {
			input := buf[:n]
			_, _ = channel.Write(input)
			if string(input) == "exit\r\n" || string(input) == "exit\n" {
				break
			}
			if string(input) == "\x03" { // ctrl-c
				_, _ = channel.Write([]byte("^C\r\nPS C:\\> "))
			}
		}
		if err != nil {
			break
		}
	}
}

func (s *FakeSSHServer) runMode(channel gossh.Channel) {
	switch s.mode {
	case ModeEcho:
		s.runEchoMode(channel)
	case ModeStallInput:
		// Stalls without reading from channel
		_, _ = channel.Write([]byte("STALLING\r\n"))
		time.Sleep(2 * time.Second)
	case ModeExitEarly:
		_, _ = channel.Write([]byte("exiting immediately\r\n"))
	case ModeOSC52:
		// Send malicious OSC 52 sequence followed by clean text
		_, _ = channel.Write([]byte("\x1b]52;c;c2VjcmV0\x07Normal Prompt> "))
	case ModeSplitRune:
		// Send 4-byte emoji split in halves
		// Emoji: 🔒 (\xF0\x9F\x94\x92)
		_, _ = channel.Write([]byte{0xF0, 0x9F})
		time.Sleep(50 * time.Millisecond)
		_, _ = channel.Write([]byte{0x94, 0x92, '\r', '\n'})
	case ModeFlood:
		for range 5000 {
			_, err := channel.Write([]byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\r\n"))
			if err != nil {
				break
			}
		}
	}
}

func (s *FakeSSHServer) sendExitStatus(channel gossh.Channel) {
	s.mu.Lock()
	code := s.exitCode
	s.mu.Unlock()

	status := struct {
		ExitStatus uint32
	}{
		ExitStatus: uint32(code),
	}
	_, _ = channel.SendRequest("exit-status", false, gossh.Marshal(&status))
}
