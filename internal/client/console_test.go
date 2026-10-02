package client

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestConsoleScreenshotLargeFramePreserved(t *testing.T) {
	data := bytes.Repeat([]byte{42}, 2<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/console/screenshot" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Error("incorrect request route or auth")
		}
		_ = json.NewEncoder(w).Encode(domain.ConsoleFrame{Data: data, MIMEType: "image/png"})
	}))
	defer srv.Close()
	got, err := New(srv.URL, "synthetic-token").ConsoleScreenshot(t.Context(), app.ConsoleScreenshotRequest{Width: 1024, Height: 768})
	if err != nil || !bytes.Equal(got.Data, data) {
		t.Fatalf("large screenshot bytes=%d err=%v", len(got.Data), err)
	}
}

func TestConsoleResponseLimitDoesNotBroadenGenericResponses(t *testing.T) {
	body := []byte(`{"text":"` + string(bytes.Repeat([]byte{'x'}, 2<<20)) + `"}`)
	var out struct {
		Text string `json:"text"`
	}
	if err := decodeHTTPResponse(t.Context(), bytes.NewReader(body), &out); err == nil {
		t.Fatal("generic response exceeded original bound")
	}
	frameBody, _ := json.Marshal(domain.ConsoleFrame{Data: bytes.Repeat([]byte{1}, 7<<20)})
	var frame domain.ConsoleFrame
	if err := decodeHTTPResponse(t.Context(), bytes.NewReader(frameBody), &frame); err == nil {
		t.Fatal("console response exceeded its bound")
	}
}
