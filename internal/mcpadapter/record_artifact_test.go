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
	"reflect"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
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
	gifBytes := createSyntheticRecordGIF(t, 16, 8)
	digest := sha256.Sum256(gifBytes)
	sha := hex.EncodeToString(digest[:])
	observed := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	recording := app.ConsoleRecording{
		MIMEType: "image/gif", SHA256: sha, Width: 16, Height: 8,
		ObservedAt: []time.Time{observed, observed.Add(100 * time.Millisecond)}, Data: gifBytes,
	}
	wantRequest := app.ConsoleRecordRequest{Target: "test-vm", Width: 16, Height: 8, Frames: 2, IntervalMillis: 100}
	a := desktopMCPServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req app.ConsoleRecordRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request body: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		if r.URL.Path != "/v1/console/record" || req != wantRequest {
			t.Errorf("record request=%+v route=%s", req, r.URL.Path)
		}
		if err := json.NewEncoder(w).Encode(recording); err != nil {
			t.Error(err)
		}
	})
	result, metadata, err := a.ConsoleRecord(t.Context(), nil, wantRequest)

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
	if img.MIMEType != "image/gif" || !bytes.Equal(img.Data, gifBytes) {
		t.Fatal("GIF content changed")
	}

	recording.Data = nil
	if !reflect.DeepEqual(metadata, recording) {
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

func TestConsoleRecordBadRequestReturnsToolError(t *testing.T) {
	a := desktopMCPServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid recording bounds", http.StatusBadRequest)
	})
	result, _, err := a.ConsoleRecord(t.Context(), nil, app.ConsoleRecordRequest{
		Width: 16, Height: 8, Frames: 2,
	})
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("expected tool error on daemon rejection: result=%+v err=%v", result, err)
	}
}
