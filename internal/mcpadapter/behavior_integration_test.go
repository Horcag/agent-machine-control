package mcpadapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type interceptTransport struct {
	mcp.Transport
	buf *bytes.Buffer
}

func (it *interceptTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	conn, err := it.Transport.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &interceptConn{Connection: conn, buf: it.buf}, nil
}

type interceptConn struct {
	mcp.Connection
	buf *bytes.Buffer
}

func (c *interceptConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	msg, err := c.Connection.Read(ctx)
	if err == nil {
		if data, errMarshal := json.Marshal(msg); errMarshal == nil {
			c.buf.Write(data)
			c.buf.WriteByte('\n')
		}
	}
	return msg, err
}

func TestBinaryStdioIntegration(t *testing.T) {
	binaryPath := resolveTestMCPBinary(t)

	stateDir := t.TempDir()
	createTestAgentToken(t, stateDir)

	cmd := exec.Command(binaryPath, "--state-dir", stateDir)

	transport := &mcp.CommandTransport{
		Command:           cmd,
		TerminateDuration: 2 * time.Second,
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	stdoutBuf := &bytes.Buffer{}
	it := &interceptTransport{
		Transport: transport,
		buf:       stdoutBuf,
	}

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	clientSession, err := mcpClient.Connect(ctx, it, nil)
	if err != nil {
		cleanupCommandProcess(t, cmd, 2*time.Second)
		t.Fatalf("failed to connect client session: %v", err)
	}

	toolsRes, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	if len(toolsRes.Tools) != 20 {
		t.Errorf("expected exactly 20 tools, got %d", len(toolsRes.Tools))
	}

	if err := clientSession.Close(); err != nil {
		t.Logf("Warning: client session close: %v", err)
	}

	_ = cmd.Wait()

	scanner := bufio.NewScanner(stdoutBuf)
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var js json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &js); err != nil {
			t.Errorf("detected non-MCP stdout line: %q (error: %v)", trimmed, err)
		}
	}
}

func resolveTestMCPBinary(t *testing.T) string {
	t.Helper()
	if configured := os.Getenv("AMC_TEST_MCP_BINARY"); configured != "" {
		return validateTestMCPBinary(t, configured)
	}
	binaryName := "amc-mcp"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	binaryPath := filepath.Join(t.TempDir(), binaryName)
	buildCmd := exec.Command("go", "build", "-o", binaryPath, "../../cmd/amc-mcp")
	buildCmd.Dir = "."
	buildCmd.Env = append(os.Environ(), "GOTOOLCHAIN=go1.26.7")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build amc-mcp binary: %v\nOutput: %s", err, string(out))
	}
	return validateTestMCPBinary(t, binaryPath)
}

func validateTestMCPBinary(t *testing.T, path string) string {
	t.Helper()
	absPath, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("invalid prebuilt amc-mcp path: %v", err)
	}
	expectedName := "amc-mcp"
	if runtime.GOOS == "windows" {
		expectedName += ".exe"
	}
	info, err := os.Lstat(absPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || filepath.Base(absPath) != expectedName {
		t.Fatalf("prebuilt amc-mcp path is not the expected regular binary")
	}
	return absPath
}

func cleanupCommandProcess(t *testing.T, cmd *exec.Cmd, timeout time.Duration) {
	t.Helper()
	if cmd == nil || cmd.Process == nil || cmd.ProcessState != nil {
		return
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		return
	case <-time.After(timeout):
	}
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Errorf("failed to terminate amc-mcp pid %d: %v", cmd.Process.Pid, err)
	}
	select {
	case <-done:
	case <-time.After(timeout):
		t.Errorf("amc-mcp pid %d did not exit after termination", cmd.Process.Pid)
	}
}
