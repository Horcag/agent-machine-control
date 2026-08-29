package mcpadapter

import (
	"context"
	"io"
	"os"
	"syscall"
	"testing"
)

func TestRunStdio_SignalAndCancel(t *testing.T) {
	oldStdin := os.Stdin
	oldStdout := os.Stdout
	defer func() {
		os.Stdin = oldStdin
		os.Stdout = oldStdout
	}()

	rIn, wIn, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	os.Stdin = rIn
	os.Stdout = wOut
	defer rIn.Close()
	defer wIn.Close()
	defer rOut.Close()
	defer wOut.Close()

	a := NewAdapter("")
	server := a.BuildServer()

	// 1. Signal shutdown
	ctx, cancel := context.WithCancel(t.Context())
	sigChan := make(chan os.Signal, 1)
	sigChan <- syscall.SIGINT

	code := runStdio(ctx, server, sigChan, cancel, io.Discard)
	if code != 0 {
		t.Errorf("expected exit code 0 on signal shutdown, got %d", code)
	}

	// 2. Pre-cancelled context should trigger connect error (Wait/Connect checks context)
	// Let's pass a pre-cancelled context to force Connect failure or cancel logic
	cancelledCtx, cancelNow := context.WithCancel(t.Context())
	cancelNow()
	sigChanEmpty := make(chan os.Signal, 1)

	// Since context is cancelled, Connect should return an error or cancel
	codeCancel := runStdio(cancelledCtx, server, sigChanEmpty, cancelNow, io.Discard)
	if codeCancel != 2 && codeCancel != 0 {
		t.Errorf("expected exit code 2 or 0 on cancelled context, got %d", codeCancel)
	}
}
