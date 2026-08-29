package hyperv

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

func TestDefaultExecutor_LookPath(t *testing.T) {
	execInstance := &DefaultExecutor{}

	// Existing binary
	path, err := execInstance.LookPath("sh")
	if err != nil || path == "" {
		t.Fatalf("expected to find 'sh' in PATH, got path=%q, err=%v", path, err)
	}

	// Non-existing binary
	_, err = execInstance.LookPath("nonexistent-binary-xyz-123")
	if err == nil {
		t.Fatalf("expected error for nonexistent binary")
	}
}

func TestDefaultExecutor_Execute_SuccessAndEnv(t *testing.T) {
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available in environment")
	}

	execInstance := &DefaultExecutor{}
	ctx := context.Background()

	stdout, stderr, err := execInstance.Execute(ctx, shPath, []string{"-c", "echo $TEST_VAR"}, []string{"TEST_VAR=hello_hyperv"})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if string(bytes.TrimSpace(stdout)) != "hello_hyperv" {
		t.Errorf("expected stdout 'hello_hyperv', got %q", string(stdout))
	}
	if len(stderr) != 0 {
		t.Errorf("expected empty stderr, got %q", string(stderr))
	}
}

func TestDefaultExecutor_Execute_Timeout(t *testing.T) {
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available in environment")
	}

	execInstance := &DefaultExecutor{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, _, err = execInstance.Execute(ctx, shPath, []string{"-c", "sleep 1"}, nil)
	if err == nil || !errors.Is(err, ErrCommandTimeout) {
		t.Fatalf("expected ErrCommandTimeout, got %v", err)
	}
}

func TestBoundedBuffer(t *testing.T) {
	buf := newBoundedBuffer(10)

	// Write within limit
	n, err := buf.Write([]byte("12345"))
	if err != nil || n != 5 {
		t.Fatalf("Write error: %v, n=%d", err, n)
	}
	if buf.exceeded {
		t.Errorf("expected exceeded=false")
	}
	if string(buf.Bytes()) != "12345" {
		t.Errorf("expected '12345', got %q", string(buf.Bytes()))
	}

	// Write exceeding limit
	n, err = buf.Write([]byte("6789012345"))
	if err != nil || n != 10 {
		t.Fatalf("Write error: %v, n=%d", err, n)
	}
	if !buf.exceeded {
		t.Errorf("expected exceeded=true")
	}
	if len(buf.Bytes()) != 10 {
		t.Errorf("expected len 10, got %d", len(buf.Bytes()))
	}
	if string(buf.Bytes()) != "1234567890" {
		t.Errorf("expected '1234567890', got %q", string(buf.Bytes()))
	}
}
