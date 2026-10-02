package mcpadapter

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color/palette"
	"image/gif"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func createSyntheticRecordGIF(t *testing.T, w, h int) []byte {
	t.Helper()
	img1 := image.NewPaletted(image.Rect(0, 0, w, h), palette.Plan9)
	img2 := image.NewPaletted(image.Rect(0, 0, w, h), palette.Plan9)
	anim := &gif.GIF{
		Image: []*image.Paletted{img1, img2},
		Delay: []int{10, 10},
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, anim); err != nil {
		t.Fatalf("failed to encode synthetic GIF: %v", err)
	}
	return buf.Bytes()
}

func TestConsoleRecordImageContentGIFAndNoDuplicatedDataInMetadata(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	token := createTestAgentToken(t, stateDir)

	gifBytes := createSyntheticRecordGIF(t, 16, 8)
	digest := sha256.Sum256(gifBytes)
	sha := hex.EncodeToString(digest[:])
	obs1 := time.Now().UTC().Add(-100 * time.Millisecond)
	obs2 := time.Now().UTC()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/console/record" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("unexpected request: %s %s, auth=%s", r.Method, r.URL.Path, r.Header.Get("Authorization"))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var req app.ConsoleRecordRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request body: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		if req.Width != 16 || req.Height != 8 || req.Frames != 2 || req.IntervalMillis != 100 {
			t.Errorf("unexpected record request: %+v", req)
		}

		_ = json.NewEncoder(w).Encode(app.ConsoleRecording{
			MIMEType:   "image/gif",
			SHA256:     sha,
			Width:      16,
			Height:     8,
			ObservedAt: []time.Time{obs1, obs2},
			Data:       gifBytes,
		})
	}))
	defer srv.Close()

	sd, err := statedir.Resolve(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := daemon.WriteEndpointFile(sd.DaemonDir(), daemon.EndpointRecord{
		Endpoint:      srv.URL,
		SchemaVersion: daemon.SchemaVersion,
		PID:           os.Getpid(),
	}); err != nil {
		t.Fatal(err)
	}

	a := NewAdapter(stateDir)
	result, metadata, err := a.ConsoleRecord(t.Context(), nil, app.ConsoleRecordRequest{
		Width:          16,
		Height:         8,
		Frames:         2,
		IntervalMillis: 100,
	})

	if err != nil || result == nil || result.IsError {
		t.Fatalf("unexpected failure: result=%+v err=%v", result, err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content item, got %d", len(result.Content))
	}

	img, ok := result.Content[0].(*mcp.ImageContent)
	if !ok {
		t.Fatalf("expected ImageContent, got %T", result.Content[0])
	}
	if img.MIMEType != "image/gif" {
		t.Fatalf("expected MIMEType image/gif, got %q", img.MIMEType)
	}
	if !bytes.Equal(img.Data, gifBytes) {
		t.Fatalf("ImageContent data mismatch")
	}

	// Verify structured metadata does NOT duplicate raw image bytes
	if len(metadata.Data) != 0 {
		t.Fatalf("metadata must not contain private bytes: len(metadata.Data)=%d", len(metadata.Data))
	}
	if metadata.MIMEType != "image/gif" || metadata.SHA256 != sha || metadata.Width != 16 || metadata.Height != 8 {
		t.Fatalf("metadata fields mismatch: %+v", metadata)
	}

	metaJSON, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("failed to marshal metadata: %v", err)
	}
	if bytes.Contains(metaJSON, []byte(`"data"`)) {
		t.Fatalf("serialized metadata contains 'data' field: %s", metaJSON)
	}
}

func TestConsoleRecordMCPTransportPreservesGIF(t *testing.T) {
	gifBytes := createSyntheticRecordGIF(t, 16, 8)
	digest := sha256.Sum256(gifBytes)
	sha := hex.EncodeToString(digest[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(app.ConsoleRecording{
			MIMEType:   "image/gif",
			SHA256:     sha,
			Width:      16,
			Height:     8,
			ObservedAt: []time.Time{time.Now().UTC()},
			Data:       gifBytes,
		})
	}))
	defer srv.Close()

	a := &Adapter{client: client.New(srv.URL, "synthetic-token")}
	ct, st := mcp.NewInMemoryTransports()
	ss, err := a.BuildServer().Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "record-test", Version: "1"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "console_record",
		Arguments: map[string]any{
			"width":           16,
			"height":          8,
			"frames":          2,
			"interval_millis": 100,
		},
	})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("CallTool failed: result=%+v err=%v", result, err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content item, got %d", len(result.Content))
	}

	img, ok := result.Content[0].(*mcp.ImageContent)
	if !ok || img.MIMEType != "image/gif" || !bytes.Equal(img.Data, gifBytes) {
		t.Fatalf("GIF content altered across MCP transport: %+v", img)
	}

	// Structured content must contain metadata without raw bytes
	metaJSON, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("failed to marshal StructuredContent: %v", err)
	}
	if bytes.Contains(metaJSON, []byte(`"data"`)) {
		t.Fatalf("StructuredContent must not include duplicated 'data' bytes: %s", metaJSON)
	}
	if !bytes.Contains(metaJSON, []byte(sha)) || !bytes.Contains(metaJSON, []byte("image/gif")) {
		t.Fatalf("StructuredContent missing expected metadata: %s", metaJSON)
	}
}

func TestConsoleRecordErrorHandling(t *testing.T) {
	// 1. Daemon returns error
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid recording bounds", http.StatusBadRequest)
	}))
	defer srv.Close()

	a := &Adapter{client: client.New(srv.URL, "synthetic-token")}
	result, _, err := a.ConsoleRecord(t.Context(), nil, app.ConsoleRecordRequest{
		Width:  16,
		Height: 8,
		Frames: 2,
	})
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("expected error result on daemon failure, got result=%+v err=%v", result, err)
	}

	// 2. Client discovery fails
	emptyAdapter := NewAdapter(t.TempDir())
	result2, _, err2 := emptyAdapter.ConsoleRecord(t.Context(), nil, app.ConsoleRecordRequest{
		Width:  16,
		Height: 8,
		Frames: 2,
	})
	if err2 != nil || result2 == nil || !result2.IsError {
		t.Fatalf("expected error result on discovery failure, got result=%+v err=%v", result2, err2)
	}
}
