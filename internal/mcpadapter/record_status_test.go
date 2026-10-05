package mcpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRecordingStatusMCPTransportMetadataOnly(t *testing.T) {
	req := app.ConsoleRecordStatusRequest{Target: "default", RecordingID: "a123456789abcdef0123456789abcdef"}
	want := app.ConsoleRecordStatus{SchemaVersion: "1", RecordingID: req.RecordingID, VMID: "local:synthetic", RequestedFrames: 2, AttemptedCaptures: 1, CaptureInFlight: true}
	a := desktopMCPServer(t, func(w http.ResponseWriter, r *http.Request) {
		var got app.ConsoleRecordStatusRequest
		if json.NewDecoder(r.Body).Decode(&got) != nil || got != req || r.URL.Path != "/v1/console/record/status" {
			t.Errorf("status request %s %+v", r.URL.Path, got)
		}
		_ = json.NewEncoder(w).Encode(want)
	})
	result := callDesktopMCPTool(t, a, "console_record_status", req)
	if result.IsError {
		t.Fatalf("status error: %+v", result)
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var got app.ConsoleRecordStatus
	if json.Unmarshal(data, &got) != nil || got != want {
		t.Fatalf("serialized status %s", data)
	}
}

func TestRecordingStatusCanceledContextDoesNotBecomeTerminal(t *testing.T) {
	a := desktopMCPServer(t, func(_ http.ResponseWriter, _ *http.Request) { t.Error("canceled request dispatched") })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, out, err := a.ConsoleRecordStatus(ctx, nil, app.ConsoleRecordStatusRequest{Target: "default", RecordingID: "a123456789abcdef0123456789abcdef"})
	if err != nil || result == nil || !result.IsError || out.Terminal {
		t.Fatalf("canceled status: %+v %+v %v", result, out, err)
	}
}

func TestRecordingStatusMCPOriginalRPCCancelLeavesIndependentReadback(t *testing.T) {
	entered, canceled := make(chan struct{}), make(chan struct{})
	release, handlerFinished := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
	id := "a123456789abcdef0123456789abcdef"
	a := recordingCancellationMCPServer(t, id, entered, canceled, release, handlerFinished)
	cs := recordingMCPClient(t, a)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	finished := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		releaseHandler()
		awaitMCPRecordingCleanup(t, finished, "MCP caller")
		select {
		case <-entered:
			awaitMCPRecordingCleanup(t, handlerFinished, "HTTP producer")
		default:
		}
	})
	go func() {
		defer close(finished)
		_, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "console_record", Arguments: app.ConsoleRecordRequest{Target: "default", RecordingID: id, Width: 8, Height: 4, Frames: 2, IntervalMillis: 100}})
		done <- err
	}()
	awaitMCPRecordingStart(t, entered, done)
	cancel()
	if err := awaitMCPRecordingResult(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("original RPC cancel %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("MCP cancellation did not reach HTTP producer")
	}
	readCtx, readCancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer readCancel()
	result, err := cs.CallTool(readCtx, &mcp.CallToolParams{Name: "console_record_status", Arguments: app.ConsoleRecordStatusRequest{Target: "default", RecordingID: id}})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("independent status %v %+v", err, result)
	}
	data, _ := json.Marshal(result.StructuredContent)
	var out app.ConsoleRecordStatus
	if json.Unmarshal(data, &out) != nil || !out.Terminal || out.TerminalReason != "canceled" || out.RecordingID != id {
		t.Fatalf("readback %s", data)
	}
}

func recordingMCPClient(t *testing.T, a *Adapter) *mcp.ClientSession {
	t.Helper()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := a.BuildServer().Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "record-status-test", Version: "1"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	return cs
}

func TestRecordingStatusMCPInconclusiveErrorCategoryIsFixed(t *testing.T) {
	a := desktopMCPServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"category":"recording_status_inconclusive","message":"private guest secret /private/path"}}`))
	})
	result, out, err := a.ConsoleRecordStatus(t.Context(), nil, app.ConsoleRecordStatusRequest{Target: "default", RecordingID: "a123456789abcdef0123456789abcdef"})
	if err != nil || result == nil || !result.IsError || out.Terminal {
		t.Fatalf("inconclusive result %+v %v", result, err)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || text.Text != "recording_status_inconclusive: recording status is inconclusive" {
		t.Fatalf("unredacted status error %+v", result.Content)
	}
}

func awaitMCPRecordingCleanup(t *testing.T, event <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(5 * time.Second):
		t.Errorf("owned %s did not stop during cleanup", name)
	}
}

func recordingCancellationMCPServer(t *testing.T, id string, entered, canceled, release, handlerFinished chan struct{}) *Adapter {
	t.Helper()
	a := desktopMCPServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/console/record":
			defer close(handlerFinished)
			var req app.ConsoleRecordRequest
			if json.NewDecoder(r.Body).Decode(&req) != nil || req.RecordingID != id {
				t.Error("producer ID lost")
			}
			close(entered)
			select {
			case <-r.Context().Done():
				close(canceled)
			case <-release:
			}
		case "/v1/console/record/status":
			_ = json.NewEncoder(w).Encode(app.ConsoleRecordStatus{SchemaVersion: "1", RecordingID: id, VMID: "local:synthetic", RequestedFrames: 2, AttemptedCaptures: 1, Terminal: true, TerminalReason: "canceled"})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	return a
}

func awaitMCPRecordingStart(t *testing.T, entered <-chan struct{}, done <-chan error) {
	t.Helper()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("MCP caller ended before HTTP producer: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("MCP producer did not begin")
	}
}

func awaitMCPRecordingResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("MCP caller did not return on cancellation")
		return nil
	}
}
