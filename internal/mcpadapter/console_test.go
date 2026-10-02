package mcpadapter

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestConsoleScreenshotImageBytesAndMetadata(t *testing.T) {
	stateDir := t.TempDir()
	token := createTestAgentToken(t, stateDir)
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 8, 4))); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/console/screenshot" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(domain.ConsoleFrame{Data: pngBytes.Bytes(), MIMEType: "image/png", FrameID: "synthetic-frame", Width: 8, Height: 4})
	}))
	defer srv.Close()
	sd, err := statedir.Resolve(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := daemon.WriteEndpointFile(sd.DaemonDir(), daemon.EndpointRecord{Endpoint: srv.URL, SchemaVersion: daemon.SchemaVersion, PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	a := NewAdapter(stateDir)
	result, metadata, err := a.ConsoleScreenshot(t.Context(), nil, ConsoleScreenshotInput{Width: 8, Height: 4})
	if err != nil || result.IsError || len(result.Content) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	img, ok := result.Content[0].(*mcp.ImageContent)
	if !ok || img.MIMEType != "image/png" || !bytes.Equal(img.Data, pngBytes.Bytes()) {
		t.Fatalf("image %+v", img)
	}
	if metadata.Frame.FrameID != "synthetic-frame" {
		t.Fatalf("metadata %+v", metadata)
	}
}

func TestConsoleScreenshotMCPTransportPreservesPNG(t *testing.T) {
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 8, 4))); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(domain.ConsoleFrame{Data: pngBytes.Bytes(), MIMEType: "image/png", FrameID: "synthetic-frame", Width: 8, Height: 4})
	}))
	defer srv.Close()
	a := &Adapter{client: client.New(srv.URL, "synthetic-token")}
	ct, st := mcp.NewInMemoryTransports()
	ss, err := a.BuildServer().Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "console-test", Version: "1"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "console_screenshot", Arguments: map[string]any{"width": 8, "height": 4}})
	if err != nil || result.IsError || len(result.Content) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	img, ok := result.Content[0].(*mcp.ImageContent)
	if !ok || !bytes.Equal(img.Data, pngBytes.Bytes()) {
		t.Fatal("PNG bytes changed through MCP transport")
	}
	metadata, err := json.Marshal(result.StructuredContent)
	if err != nil || bytes.Contains(metadata, []byte(`"data"`)) || !bytes.Contains(metadata, []byte("synthetic-frame")) {
		t.Fatalf("structured metadata %s err=%v", metadata, err)
	}
}

func TestConsoleScreenshotInvalidDimensionsAndNoFallback(t *testing.T) {
	a := NewAdapter(t.TempDir())
	for _, in := range []ConsoleScreenshotInput{{Width: -1, Height: 1}, {Width: 1, Height: 0}, {Width: 4096, Height: 4096}, {Width: 8, Height: 4}} {
		result, _, err := a.ConsoleScreenshot(t.Context(), nil, in)
		if err != nil || result == nil || !result.IsError {
			t.Fatalf("input=%+v result=%+v err=%v", in, result, err)
		}
	}
}

func TestConsoleInputPreservesExactMetadataAndServerErrors(t *testing.T) {
	in := ConsoleInputInput{Input: domain.ConsoleInput{Kind: "click", FrameID: "synthetic-frame", X: 4, Y: 2, Button: "left"}, Reason: "exercise console", IdempotencyKey: "synthetic-key", Deadline: "2026-10-02T20:00:00Z", ApprovalID: "synthetic-approval"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got ConsoleInputInput
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		if got != in || r.URL.Path != "/v1/console/input" || r.Header.Get("Authorization") != "Bearer synthetic-agent-token" {
			t.Errorf("request changed: %+v", got)
		}
		http.Error(w, "invalid coordinate", http.StatusBadRequest)
	}))
	defer srv.Close()
	a := &Adapter{client: client.New(srv.URL, "synthetic-agent-token")}
	result, _, err := a.ConsoleInput(t.Context(), nil, in)
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
