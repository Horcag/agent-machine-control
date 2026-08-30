package sessions_test

import (
	"context"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/sessions"
)

func TestWaitSettle(t *testing.T) {
	buf := sessions.NewRingBuffer(1024)
	now := time.Now().UTC()

	// Append initial output
	buf.Append("Command starting...\n", now)

	go func() {
		time.Sleep(50 * time.Millisecond)
		buf.Append("Command running...\n", now)
		time.Sleep(50 * time.Millisecond)
		buf.Append("Command finished.\n", now)
	}()

	// Wait for 100ms settle window
	ctx := context.Background()
	chunks, nextSeq, _, err := sessions.WaitSettle(ctx, buf, 100*time.Millisecond, 0, 2*time.Second)
	if err != nil {
		t.Fatalf("WaitSettle failed: %v", err)
	}

	if len(chunks) != 3 {
		t.Errorf("expected 3 chunks after settle, got %d", len(chunks))
	}
	if nextSeq != 3 {
		t.Errorf("expected nextSeq 3, got %d", nextSeq)
	}
}

func TestWaitRegex_Success(t *testing.T) {
	buf := sessions.NewRingBuffer(1024)
	now := time.Now().UTC()

	go func() {
		time.Sleep(50 * time.Millisecond)
		buf.Append("PS C:\\Users\\Administrator> ", now)
	}()

	ctx := context.Background()
	chunks, nextSeq, _, matched, err := sessions.WaitRegex(ctx, buf, `PS [A-Z]:\\.*>`, 0, 2*time.Second)
	if err != nil {
		t.Fatalf("WaitRegex failed: %v", err)
	}
	if !matched {
		t.Error("expected matched to be true")
	}
	if len(chunks) == 0 {
		t.Error("expected at least 1 chunk")
	}
	if nextSeq == 0 {
		t.Error("expected valid nextSeq")
	}
}

func TestWaitRegex_Timeout(t *testing.T) {
	buf := sessions.NewRingBuffer(1024)

	ctx := context.Background()
	_, _, _, matched, err := sessions.WaitRegex(ctx, buf, `NEVER_MATCHES`, 0, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if matched {
		t.Error("expected matched false on timeout")
	}
}

func TestWait_InvalidRegexAndCanceledContext(t *testing.T) {
	buf := sessions.NewRingBuffer(1024)

	// Invalid regex
	ctx := context.Background()
	_, _, _, _, err := sessions.WaitRegex(ctx, buf, `[unclosed-bracket`, 0, time.Second)
	if err == nil {
		t.Errorf("expected error on invalid regex syntax")
	}

	// Canceled context on WaitSettle
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, _, err = sessions.WaitSettle(canceledCtx, buf, time.Second, 0, time.Second)
	if err == nil {
		t.Errorf("expected error on canceled context")
	}

	// Settle timeout
	_, _, _, err = sessions.WaitSettle(context.Background(), buf, 500*time.Millisecond, 0, 50*time.Millisecond)
	if err == nil {
		t.Errorf("expected timeout on WaitSettle")
	}

	// Pattern exceeds max length
	longPattern := make([]byte, 1025)
	for i := range longPattern {
		longPattern[i] = 'a'
	}
	_, _, _, _, err = sessions.WaitRegex(context.Background(), buf, string(longPattern), 0, time.Second)
	if err == nil {
		t.Errorf("expected error on overly long regex pattern")
	}

	// Immediate regex match in existing buffer
	buf.Append("immediate-match-content\n", time.Now().UTC())
	_, _, _, matched, err := sessions.WaitRegex(context.Background(), buf, "immediate-match", 0, time.Second)
	if err != nil || !matched {
		t.Errorf("expected immediate match on existing buffer: %v", err)
	}
}
