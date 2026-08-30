package mcpadapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
	tmpDir := t.TempDir()
	binaryPath := filepath.Join(tmpDir, "amc-mcp")
	buildCmd := exec.Command("go", "build", "-o", binaryPath, "../../cmd/amc-mcp")
	buildCmd.Dir = "."
	buildCmd.Env = append(os.Environ(), "GOTOOLCHAIN=go1.26.6")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build amc-mcp binary: %v\nOutput: %s", err, string(out))
	}

	stateDir := t.TempDir()
	authDir := filepath.Join(stateDir, "auth")
	if err := os.MkdirAll(authDir, 0700); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}
	tokenPath := filepath.Join(authDir, "agent-mcp.token")
	if err := os.WriteFile(tokenPath, []byte(validTestToken+"\n"), 0600); err != nil {
		t.Fatalf("failed to write token: %v", err)
	}

	cmd := exec.Command(binaryPath, "--state-dir", stateDir)

	transport := &mcp.CommandTransport{
		Command:           cmd,
		TerminateDuration: 2 * time.Second,
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	stdoutBuf := &bytes.Buffer{}
	it := &interceptTransport{
		Transport: transport,
		buf:       stdoutBuf,
	}

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	clientSession, err := mcpClient.Connect(ctx, it, nil)
	if err != nil {
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
