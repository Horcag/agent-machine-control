package mcpadapter

import (
	"context"
	"io"
	"net"
	"os"
	"syscall"
	"testing"
)

func TestRunHTTP_BindFailure(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to bind test listener: %v", err)
	}
	defer l.Close()
	addr := l.Addr().String()

	// Need a valid stateDir to pass the resolution and token checks
	tempDir := t.TempDir()
	createTestAgentToken(t, tempDir)

	a := NewAdapter("")
	server := a.BuildServer()
	ctx := t.Context()
	sigChan := make(chan os.Signal, 1)

	code := runHTTP(ctx, server, tempDir, addr, sigChan, io.Discard)
	if code != 2 {
		t.Errorf("expected exit code 2 on bind failure, got %d", code)
	}
}

func TestRunHTTP_CleanContextShutdownAndSignal(t *testing.T) {
	tempDir := t.TempDir()
	createTestAgentToken(t, tempDir)

	a := NewAdapter("")
	server := a.BuildServer()

	// 1. Test clean context shutdown
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	addr := l.Addr().String()
	l.Close()

	sigChan := make(chan os.Signal, 1)
	code := runHTTP(ctx, server, tempDir, addr, sigChan, io.Discard)
	if code != 0 {
		t.Errorf("expected exit code 0 on pre-canceled context, got %d", code)
	}

	// 2. Test signal shutdown
	ctx = t.Context()
	sigChan = make(chan os.Signal, 1)
	sigChan <- syscall.SIGINT

	code = runHTTP(ctx, server, tempDir, addr, sigChan, io.Discard)
	if code != 0 {
		t.Errorf("expected exit code 0 on signal shutdown, got %d", code)
	}
}
