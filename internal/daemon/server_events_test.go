package daemon_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestServer_GlobalEvents_SSE(t *testing.T) {
	srv, endpoint, opToken, agentToken := setupTestServer(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	// 1. Agent token should be forbidden (403) for global events
	assertAgentForbiddenOnEvents(t, endpoint, agentToken)

	// 2. Operator token should succeed with text/event-stream
	opResp := connectOperatorSSE(t, endpoint, opToken)
	defer opResp.Body.Close()

	// Submit an operation
	targetID := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	createBody := daemon.CreateOperationRequest{
		Kind:           "machine.start",
		Target:         targetID,
		Reason:         "test sse global",
		IdempotencyKey: "idem-sse-global-1",
	}
	data, _ := json.Marshal(createBody)
	postReq, _ := http.NewRequest(http.MethodPost, endpoint+"/v1/operations", bytes.NewReader(data))
	postReq.Header.Set("Authorization", "Bearer "+opToken)
	postReq.Header.Set("Content-Type", "application/json")
	postResp, err := http.DefaultClient.Do(postReq)
	if err != nil {
		t.Fatalf("create operation failed: %v", err)
	}
	defer postResp.Body.Close()

	if !waitForSSEEventTarget(opResp.Body, domain.MachineRef(targetID)) {
		t.Errorf("expected to receive operation event on global SSE stream")
	}
}

func assertAgentForbiddenOnEvents(t *testing.T, endpoint, agentToken string) {
	t.Helper()
	agentReq, _ := http.NewRequest(http.MethodGet, endpoint+"/v1/events", nil)
	agentReq.Header.Set("Authorization", "Bearer "+agentToken)
	agentResp, err := http.DefaultClient.Do(agentReq)
	if err != nil {
		t.Fatalf("agent /v1/events request failed: %v", err)
	}
	defer agentResp.Body.Close()
	if agentResp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden for agent token on /v1/events, got %d", agentResp.StatusCode)
	}
}

func connectOperatorSSE(t *testing.T, endpoint, opToken string) *http.Response {
	t.Helper()
	opReq, _ := http.NewRequest(http.MethodGet, endpoint+"/v1/events", nil)
	opReq.Header.Set("Authorization", "Bearer "+opToken)
	opReq.Header.Set("Accept", "text/event-stream")

	client := &http.Client{Timeout: 0}
	opResp, err := client.Do(opReq)
	if err != nil {
		t.Fatalf("operator /v1/events request failed: %v", err)
	}
	if opResp.StatusCode != http.StatusOK {
		_ = opResp.Body.Close()
		t.Fatalf("expected 200 OK for /v1/events, got %d", opResp.StatusCode)
	}
	if ct := opResp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		_ = opResp.Body.Close()
		t.Errorf("expected text/event-stream Content-Type, got %s", ct)
	}
	return opResp
}

func waitForSSEEventTarget(body io.Reader, target domain.MachineRef) bool {
	reader := bufio.NewReader(body)
	var receivedEvent bool
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		for range 20 {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			if dataStr, ok := strings.CutPrefix(line, "data:"); ok {
				var ev domain.Event
				if err := json.Unmarshal([]byte(strings.TrimSpace(dataStr)), &ev); err == nil {
					if ev.Target == target {
						receivedEvent = true
						return
					}
				}
			}
		}
	}()

	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
	}
	return receivedEvent
}

func TestServer_ListenAddr_LoopbackValidation(t *testing.T) {
	dir := t.TempDir()
	srv, err := daemon.NewServer(daemon.Config{
		StateDir:   dir,
		ListenAddr: "0.0.0.0:8080",
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	err = srv.Start()
	if err == nil {
		_ = srv.Shutdown(context.Background())
		t.Errorf("expected Start to fail for non-loopback listen address 0.0.0.0:8080")
	}
}

func TestServer_RequestBodyBoundsAndContentType(t *testing.T) {
	srv, endpoint, opToken, _ := setupTestServer(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	// 1. Invalid Content-Type
	req, _ := http.NewRequest(http.MethodPost, endpoint+"/v1/operations", strings.NewReader(`{"kind":"machine.start"}`))
	req.Header.Set("Authorization", "Bearer "+opToken)
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("expected 415 Unsupported Media Type for text/plain, got %d", resp.StatusCode)
	}

	// 2. Oversize body (> 64 KB)
	largeReason := strings.Repeat("A", 70*1024)
	largeBody := fmt.Sprintf(`{"kind":"machine.start","target":"c4a523d4-6b99-4d62-a5e2-4752c0f20001","reason":%q}`, largeReason)
	req2, _ := http.NewRequest(http.MethodPost, endpoint+"/v1/operations", strings.NewReader(largeBody))
	req2.Header.Set("Authorization", "Bearer "+opToken)
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("oversize request failed: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for oversize payload, got %d", resp2.StatusCode)
	}

	// 3. Unauthenticated /v1/events
	unauthReq, _ := http.NewRequest(http.MethodGet, endpoint+"/v1/events", nil)
	unauthResp, err := http.DefaultClient.Do(unauthReq)
	if err != nil {
		t.Fatalf("unauthenticated /v1/events failed: %v", err)
	}
	defer unauthResp.Body.Close()
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for missing token on /v1/events, got %d", unauthResp.StatusCode)
	}
}

func TestServer_GlobalEvents_ShutdownAndHub(t *testing.T) {
	srv, endpoint, opToken, _ := setupTestServer(t)

	opResp := connectOperatorSSE(t, endpoint, opToken)
	defer opResp.Body.Close()

	// Trigger shutdown while SSE stream is open
	go func() {
		time.Sleep(30 * time.Millisecond)
		srv.TriggerShutdown()
		_ = srv.Shutdown(context.Background())
	}()

	reader := bufio.NewReader(opResp.Body)
	for {
		_, err := reader.ReadString('\n')
		if err != nil {
			break
		}
	}
	srv.Wait()
}
