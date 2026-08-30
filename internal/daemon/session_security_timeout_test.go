package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

type deadlineCaptureTransport struct {
	mu        sync.Mutex
	remaining map[string]time.Duration
}

func (t *deadlineCaptureTransport) record(ctx context.Context, name string) {
	deadline, ok := ctx.Deadline()
	t.mu.Lock()
	defer t.mu.Unlock()
	if !ok {
		t.remaining[name] = -1
		return
	}
	t.remaining[name] = time.Until(deadline)
}

func (t *deadlineCaptureTransport) remainingFor(name string) (time.Duration, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	remaining, ok := t.remaining[name]
	return remaining, ok
}

func assertSubSecondTransportOutcome(t *testing.T, transport *deadlineCaptureTransport, operation string, status int) bool {
	t.Helper()
	remaining, called := transport.remainingFor(operation)
	switch status {
	case http.StatusOK:
		if !called || remaining <= 0 || remaining > 250*time.Millisecond {
			t.Fatalf("%s transport deadline remaining=%v present=%v, want (0, 250ms]", operation, remaining, called)
		}
		return true
	case http.StatusGatewayTimeout:
		if called {
			t.Fatalf("%s transport was called after admission exhausted its budget: remaining=%v", operation, remaining)
		}
		return false
	default:
		t.Fatalf("%s status=%d, want 200 with positive transport budget or 504 with zero transport effect", operation, status)
		return false
	}
}

func (t *deadlineCaptureTransport) Dial(ctx context.Context, _ domain.MachineRef, _, _ uint16, _ string) (guestssh.Channel, error) {
	t.record(ctx, "open")
	reader, writer := io.Pipe()
	channel := &deadlineCaptureChannel{parent: t, reader: reader, writer: writer, waitCh: make(chan struct{})}
	go func() { _, _ = writer.Write([]byte("synthetic prompt> ")) }()
	return channel, nil
}

type deadlineCaptureChannel struct {
	parent *deadlineCaptureTransport
	reader *io.PipeReader
	writer *io.PipeWriter
	waitCh chan struct{}
	once   sync.Once
}

func (c *deadlineCaptureChannel) Read(p []byte) (int, error) { return c.reader.Read(p) }
func (c *deadlineCaptureChannel) Write(ctx context.Context, p []byte) (int, error) {
	c.parent.record(ctx, "write")
	return len(p), nil
}
func (c *deadlineCaptureChannel) SendControl(ctx context.Context, _ domain.ControlKey) (guestssh.ControlResult, error) {
	c.parent.record(ctx, "control")
	return guestssh.ControlResult{AcceptedBytes: 1, EffectApplied: true}, nil
}
func (c *deadlineCaptureChannel) Resize(uint16, uint16) error { return nil }
func (c *deadlineCaptureChannel) Close(ctx context.Context) error {
	c.once.Do(func() {
		c.parent.record(ctx, "close")
		_ = c.writer.Close()
		_ = c.reader.Close()
		close(c.waitCh)
	})
	return nil
}
func (c *deadlineCaptureChannel) Wait() (int, error) {
	<-c.waitCh
	return 0, nil
}

func openSanitizerSession(t *testing.T, endpoint, operatorToken string) string {
	t.Helper()
	status, data := doJSONReq(t, http.MethodPost, endpoint+"/v1/sessions", operatorToken, daemon.SessionOpenRequest{
		Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "open sanitizer test",
		IdempotencyKey: "sanitizer-open", TimeoutSeconds: 2,
	})
	if status != http.StatusOK {
		t.Fatalf("open sanitizer session status=%d body=%s", status, data)
	}
	var opened daemon.SessionOpenResponse
	if err := json.Unmarshal(data, &opened); err != nil {
		t.Fatal(err)
	}
	return endpoint + "/v1/sessions/" + opened.Session.SessionID
}

func writeSplitSecrets(t *testing.T, sessionPath, operatorToken string, exactValues []string) {
	t.Helper()
	writeIndex := 0
	writeChunk := func(chunk string) {
		t.Helper()
		writeIndex++
		status, body := doJSONReq(t, http.MethodPost, sessionPath+"/write", operatorToken, daemon.SessionWriteRequest{
			Data: chunk, Reason: "exercise exact sanitizer boundary", IdempotencyKey: fmt.Sprintf("sanitizer-write-%d", writeIndex), TimeoutSeconds: 2,
		})
		if status != http.StatusOK {
			t.Fatalf("sanitizer write %d status=%d body=%s", writeIndex, status, body)
		}
	}
	for i, secret := range exactValues {
		split := 7 + i*5
		writeChunk(secret[:split])
		writeChunk(secret[split:])
	}
	ordinary := "ordinary-output-start " + strings.Repeat("z", 96) + " ordinary-output-end " + strings.Repeat("q", 96)
	writeChunk(ordinary)
}

func waitForSanitizedOutput(t *testing.T, sessionPath, operatorToken string) string {
	t.Helper()
	status, data := doJSONReq(t, http.MethodPost, sessionPath+"/wait", operatorToken, daemon.SessionWaitRequest{
		Regex: "ordinary-output-end", TimeoutMillis: 1000,
	})
	if status != http.StatusOK {
		t.Fatalf("wait sanitizer output status=%d body=%s", status, data)
	}
	var waited daemon.SessionWaitResponse
	if err := json.Unmarshal(data, &waited); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	for _, chunk := range waited.Chunks {
		output.WriteString(chunk.Data)
	}
	return output.String()
}

func assertExactValuesRedacted(t *testing.T, clean string, exactValues []string) {
	t.Helper()
	for _, secret := range exactValues {
		if strings.Contains(clean, secret) {
			t.Fatal("session output exposed an exact server-owned secret")
		}
	}
	if !strings.Contains(clean, "ordinary-output-start") || !strings.Contains(clean, "ordinary-output-end") {
		t.Fatal("ordinary session output was not preserved")
	}
	if strings.Count(clean, "[REDACTED]") < 3 {
		t.Fatalf("expected all exact secrets to be redacted, redaction count=%d", strings.Count(clean, "[REDACTED]"))
	}
}

func closeSanitizerSession(t *testing.T, sessionPath, operatorToken string) {
	t.Helper()
	status, data := doJSONReq(t, http.MethodPost, sessionPath+"/close", operatorToken, daemon.SessionCloseRequest{
		Reason: "close sanitizer test", IdempotencyKey: "sanitizer-close", TimeoutSeconds: 2,
	})
	if status != http.StatusOK {
		t.Fatalf("close sanitizer session status=%d body=%s", status, data)
	}
}

func containsToken(data []byte, tokens []string) bool {
	for _, token := range tokens {
		if bytes.Contains(data, []byte(token)) {
			return true
		}
	}
	return false
}

func scanEvidenceDirForTokens(rootPath string, tokens []string) error {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return err
	}
	walkErr := filepath.WalkDir(rootPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		relativePath, err := filepath.Rel(rootPath, path)
		if err != nil {
			return err
		}
		persisted, err := root.ReadFile(relativePath)
		if err != nil {
			return err
		}
		if containsToken(persisted, tokens) {
			return errors.New("active bearer token copied into session evidence storage")
		}
		return nil
	})
	return errors.Join(walkErr, root.Close())
}

func assertTokensAbsentFromEvidence(t *testing.T, stateDir string, tokens []string) {
	t.Helper()
	for _, relativeDir := range []string{"audit", "receipts", "sessions"} {
		if err := scanEvidenceDirForTokens(filepath.Join(stateDir, relativeDir), tokens); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDaemonSessions_ActiveBearerTokensAreMandatoryExactSecrets(t *testing.T) {
	configuredMarker := "configured-synthetic-marker"
	srv, endpoint, operatorToken, agentToken, stateDir, fakeSSH := setupTestDaemonWithSSHConfig(t, guestssh.SanitizerConfig{
		ExactSecrets: [][]byte{[]byte(configuredMarker)},
	})
	defer func() { _ = srv.Shutdown(context.Background()) }()
	defer fakeSSH.Close()

	sessionPath := openSanitizerSession(t, endpoint, operatorToken)
	exactValues := []string{operatorToken, agentToken, configuredMarker}
	writeSplitSecrets(t, sessionPath, operatorToken, exactValues)
	assertExactValuesRedacted(t, waitForSanitizedOutput(t, sessionPath, operatorToken), exactValues)
	closeSanitizerSession(t, sessionPath, operatorToken)
	assertTokensAbsentFromEvidence(t, stateDir, []string{operatorToken, agentToken})
}
