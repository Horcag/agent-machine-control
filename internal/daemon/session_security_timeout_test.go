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

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

const subSecondTarget = "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

func setupSubSecondDeadlineServer(t *testing.T, backend app.Backend) (*daemon.Server, *deadlineCaptureTransport, string) {
	t.Helper()
	dir := missingDaemonStateRoot(t)
	seedDaemonTestTarget(t, dir)
	transport := &deadlineCaptureTransport{remaining: make(map[string]time.Duration)}
	keyProvider := &guestssh.MockKeyProvider{MachineConfig: &guestssh.MachineSSHConfig{
		Endpoint: "192.0.2.20:22", User: "synthetic", DefaultKeyAlias: "default",
		PinnedHostKeySHA256: "c3ludGhldGlj", ExternalEffectsContained: true,
		RollbackCheckpointID: "e4a523d4-6b99-4d62-a5e2-4752c0f20001",
	}}
	// Fix policy timestamps while retaining real context timers and transport budgets.
	now := time.Now().UTC()
	srv, err := daemon.NewServer(daemon.Config{
		StateDir: dir, ListenAddr: "127.0.0.1:0", Backend: backend, Transport: transport,
		KeyProvider: keyProvider, Clock: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := srv.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	token, err := auth.ReadTokenFile(filepath.Join(dir, "auth"), auth.TokenTypeOperator)
	if err != nil {
		t.Fatal(err)
	}
	return srv, transport, token
}

func openSubSecondSetupSession(t *testing.T, endpoint, token string) string {
	t.Helper()
	// This setup is unmeasured; every operation under test keeps its 250ms/40ms budget.
	status, body := doJSONReq(t, http.MethodPost, endpoint+"/v1/sessions", token, daemon.SessionOpenRequest{
		Target: subSecondTarget, Reason: "unmeasured session setup", IdempotencyKey: "subsecond-setup-open", TimeoutSeconds: 30,
	})
	requireJSONOK(t, status, body, "unmeasured session setup")
	var opened daemon.SessionOpenResponse
	if err := json.Unmarshal(body, &opened); err != nil {
		t.Fatal(err)
	}
	if opened.Session.SessionID == "" {
		t.Fatalf("setup has no session ID; body=%s", body)
	}
	return opened.Session.SessionID
}

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

func assertSubSecondTransportOutcome(t *testing.T, transport *deadlineCaptureTransport, operation string, status int, body []byte) {
	t.Helper()
	remaining, called := transport.remainingFor(operation)
	switch status {
	case http.StatusOK:
		if !called || remaining <= 0 || remaining > 250*time.Millisecond {
			t.Fatalf("%s transport deadline remaining=%v present=%v, want (0, 250ms]; body=%s", operation, remaining, called, body)
		}
		return
	case http.StatusGatewayTimeout:
		if called {
			t.Fatalf("%s transport was called after admission exhausted its budget: remaining=%v; body=%s", operation, remaining, body)
		}
		return
	default:
		t.Fatalf("%s status=%d, want 200 with positive transport budget or 504 with zero transport effect; body=%s", operation, status, body)
		return
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
func (c *deadlineCaptureChannel) LastCloseOutcome() guestssh.CloseOutcome {
	select {
	case <-c.waitCh:
		return guestssh.CloseOutcome{Complete: true}
	default:
		return guestssh.CloseOutcome{}
	}
}
func (c *deadlineCaptureChannel) Wait() (int, error) {
	<-c.waitCh
	return 0, nil
}

func openSanitizerSession(t *testing.T, endpoint, operatorToken string) string {
	t.Helper()
	status, data := doJSONReq(t, http.MethodPost, endpoint+"/v1/sessions", operatorToken, daemon.SessionOpenRequest{
		Target: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "open sanitizer test",
		IdempotencyKey: "sanitizer-open", TimeoutSeconds: 30,
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
			Data: chunk, Reason: "exercise exact sanitizer boundary", IdempotencyKey: fmt.Sprintf("sanitizer-write-%d", writeIndex), TimeoutSeconds: 30,
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
		Regex: "ordinary-output-end", TimeoutMillis: 10_000,
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
		Reason: "close sanitizer test", IdempotencyKey: "sanitizer-close", TimeoutSeconds: 30,
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
