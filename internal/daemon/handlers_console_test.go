package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

type consoleDaemonBackend struct {
	*mockDaemonBackend
	mu          sync.Mutex
	inputs      []domain.ConsoleInput
	captureErr  error
	captureHook func(context.Context) error
}

func (b *consoleDaemonBackend) Capabilities(context.Context, string) (domain.CapabilitySet, error) {
	return domain.NewCapabilitySet(domain.CapabilityConsoleScreenshot, domain.CapabilityConsoleInput), nil
}
func (b *consoleDaemonBackend) CaptureConsole(ctx context.Context, id string, width, height int) (domain.ConsoleFrame, error) {
	if b.captureHook != nil {
		if err := b.captureHook(ctx); err != nil {
			return domain.ConsoleFrame{}, err
		}
	}
	if b.captureErr != nil {
		return domain.ConsoleFrame{}, b.captureErr
	}
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		return domain.ConsoleFrame{}, err
	}
	return domain.ConsoleFrame{VMID: id, Width: width, Height: height, NativeWidth: 1920, NativeHeight: 1080, Data: data.Bytes()}, nil
}
func (b *consoleDaemonBackend) SendConsoleInput(_ context.Context, _ string, input domain.ConsoleInput) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.inputs = append(b.inputs, input)
	return nil
}

func setupConsoleDaemon(t *testing.T, backend *consoleDaemonBackend) (string, string, string) {
	t.Helper()
	dir := missingDaemonStateRoot(t)
	seedDaemonTestTarget(t, dir)
	srv, err := daemon.NewServer(daemon.Config{StateDir: dir, ListenAddr: "127.0.0.1:0", Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("console daemon shutdown: %v", err)
		}
	})
	op, err := auth.ReadTokenFile(filepath.Join(dir, "auth"), auth.TokenTypeOperator)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := auth.ReadTokenFile(filepath.Join(dir, "auth"), auth.TokenTypeAgentMCP)
	if err != nil {
		t.Fatal(err)
	}
	return srv.Endpoint(), op, agent
}

func TestDaemonConsoleCaptureAndExactDelegatedInput(t *testing.T) {
	backend := &consoleDaemonBackend{mockDaemonBackend: &mockDaemonBackend{}}
	endpoint, operator, agent := setupConsoleDaemon(t, backend)
	status, body := doJSONReq(t, http.MethodPost, endpoint+"/v1/console/screenshot", agent, app.ConsoleScreenshotRequest{Width: 100, Height: 50})
	if status != http.StatusOK {
		t.Fatalf("capture: %d %s", status, body)
	}
	var frame domain.ConsoleFrame
	if err := json.Unmarshal(body, &frame); err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(bytes.NewReader(frame.Data)); err != nil || frame.FrameID == "" {
		t.Fatalf("frame: %v", err)
	}
	input := domain.ConsoleInput{Kind: "click", FrameID: frame.FrameID, X: 50, Y: 25, Button: "left"}
	req := app.ConsoleInputRequest{Input: input, Reason: "synthetic exact console action", IdempotencyKey: "daemon-console-click", Deadline: time.Now().Add(time.Minute).Format(time.RFC3339Nano)}
	denied := req
	denied.IdempotencyKey = "daemon-console-denied"
	status, _ = doJSONReq(t, http.MethodPost, endpoint+"/v1/console/input", agent, denied)
	if status != http.StatusForbidden {
		t.Fatalf("unapproved input: %d", status)
	}
	approvalRequest := daemon.OperationApprovalIssueRequest{Kind: "console.input", Target: "default", Reason: req.Reason, IdempotencyKey: req.IdempotencyKey, ValidForMillis: 60000, Beneficiary: "agent:mcp-local", Parameters: domain.ConsoleInputParameters(input)}
	status, _ = doJSONReq(t, http.MethodPost, endpoint+"/v1/operation-approvals", agent, approvalRequest)
	if status != http.StatusForbidden {
		t.Fatalf("agent issued approval: %d", status)
	}
	status, body = doJSONReq(t, http.MethodPost, endpoint+"/v1/operation-approvals", operator, approvalRequest)
	if status != http.StatusOK {
		t.Fatalf("grant: %d %s", status, body)
	}
	var grant daemon.OperationApprovalIssueResponse
	if err := json.Unmarshal(body, &grant); err != nil {
		t.Fatal(err)
	}
	req.ApprovalID, req.Deadline = grant.ApprovalID, grant.Deadline
	for range 2 {
		status, body = doJSONReq(t, http.MethodPost, endpoint+"/v1/console/input", agent, req)
		if status != http.StatusOK {
			t.Fatalf("approved input: %d %s", status, body)
		}
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.inputs) != 1 || backend.inputs[0].X != 960 || backend.inputs[0].Y != 540 {
		t.Fatalf("input/mapping/retry: %+v", backend.inputs)
	}
}

func TestDaemonConsoleRejectsMalformedAndRedactsProviderErrors(t *testing.T) {
	backend := &consoleDaemonBackend{mockDaemonBackend: &mockDaemonBackend{}, captureErr: errors.New("synthetic guest secret")}
	endpoint, token, _ := setupConsoleDaemon(t, backend)
	for _, tc := range []struct {
		method, path string
		body         any
		status       int
	}{
		{http.MethodGet, "console/screenshot", nil, http.StatusMethodNotAllowed},
		{http.MethodPost, "console/unknown", nil, http.StatusNotFound},
		{http.MethodPost, "console/screenshot", map[string]any{"actor": "operator:forged"}, http.StatusBadRequest},
		{http.MethodPost, "console/input", map[string]any{"actor": "operator:forged"}, http.StatusBadRequest},
		{http.MethodPost, "console/input", app.ConsoleInputRequest{Input: domain.ConsoleInput{Kind: "key", Key: "enter"}}, http.StatusBadRequest},
		{http.MethodPost, "console/screenshot", app.ConsoleScreenshotRequest{Target: "foreign-vm"}, http.StatusBadRequest},
		{http.MethodPost, "console/screenshot", app.ConsoleScreenshotRequest{Width: 8, Height: 8}, http.StatusBadRequest},
	} {
		status, body := doJSONReq(t, tc.method, endpoint+"/v1/"+tc.path, token, tc.body)
		if status != tc.status || bytes.Contains(body, []byte("synthetic guest secret")) {
			t.Fatalf("%s: %d %s", tc.path, status, body)
		}
	}
	status, _ := doJSONReq(t, http.MethodPost, endpoint+"/v1/console/screenshot", "", app.ConsoleScreenshotRequest{})
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated capture: %d", status)
	}
}

func TestDaemonConsoleUnavailableIsExplicit(t *testing.T) {
	srv, endpoint, token, _ := setupTestServer(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()
	status, body := doJSONReq(t, http.MethodPost, endpoint+"/v1/console/screenshot", token, app.ConsoleScreenshotRequest{})
	if status != http.StatusNotImplemented || !bytes.Contains(body, []byte("capability_unavailable")) {
		t.Fatalf("status=%d body=%s", status, body)
	}
}
