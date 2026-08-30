package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

type sessionOutputRoundTripper struct {
	response daemon.SessionReadResponse
}

func (rt sessionOutputRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	data, err := json.Marshal(rt.response)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(data)),
	}, nil
}

func unsafeSessionOutputClient() *client.Client {
	response := daemon.SessionReadResponse{
		SchemaVersion: "1",
		SessionID:     "sess-f1e2d3c4b5a6f1e2d3c4b5a6f1e2d3c4",
		Chunks: []daemon.SessionChunkDTO{
			{Seq: 1, Data: "safe\x1b[2J\x1b]2;forged"},
			{Seq: 2, Data: " title\a\x1b[?1049htext"},
		},
		NextSeq: 2,
		Closed:  true,
	}
	httpClient := &http.Client{Transport: sessionOutputRoundTripper{response: response}}
	return client.New("http://127.0.0.1:1", "test-token", client.WithHTTPClient(httpClient))
}

func TestSanitizeSessionChunksStripsSplitControlsAndMutatesDTOs(t *testing.T) {
	chunks := []daemon.SessionChunkDTO{
		{Seq: 1, Data: "safe\x1b]2;forged"},
		{Seq: 2, Data: " title\a\x1b[?1049htext"},
	}
	sanitizer := guestssh.NewStreamSanitizer(guestssh.SanitizerConfig{})
	if got := sanitizeSessionChunks(chunks, sanitizer, true); got != "safetext" {
		t.Fatalf("sanitized output = %q, want safetext", got)
	}
	if chunks[0].Data != "safe" || chunks[1].Data != "text" {
		t.Fatalf("chunk DTOs retained unsafe data: %#v", chunks)
	}
}

func TestSanitizeSessionChunksRetainsParserStateAcrossAttachPolls(t *testing.T) {
	sanitizer := guestssh.NewStreamSanitizer(guestssh.SanitizerConfig{})
	first := []daemon.SessionChunkDTO{{Seq: 1, Data: "prefix\x1bPpayload"}}
	if got := sanitizeSessionChunks(first, sanitizer, false); got != "prefix" {
		t.Fatalf("first poll output = %q, want prefix", got)
	}
	second := []daemon.SessionChunkDTO{{Seq: 2, Data: " continues\x1b\\suffix"}}
	if got := sanitizeSessionChunks(second, sanitizer, true); got != "suffix" {
		t.Fatalf("second poll output = %q, want suffix", got)
	}
}

func TestSessionReadAndAttachNeverWriteTerminalControls(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(context.Context, *client.Client, []string, io.Writer, io.Writer) int
	}{
		{name: "read", run: runSessionRead},
		{name: "attach", run: runSessionAttach},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := test.run(context.Background(), unsafeSessionOutputClient(), []string{"sess-f1e2d3c4b5a6f1e2d3c4b5a6f1e2d3c4"}, &stdout, &stderr)
			if code != ExitSuccess {
				t.Fatalf("exit code = %d: %s", code, stderr.String())
			}
			if got := stdout.String(); got != "safetext" || strings.ContainsRune(got, '\x1b') {
				t.Fatalf("writer received unsafe or corrupted output %q", got)
			}
		})
	}
}

func TestSessionReadJSONContainsOnlySanitizedChunkData(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runSessionRead(
		context.Background(),
		unsafeSessionOutputClient(),
		[]string{"sess-f1e2d3c4b5a6f1e2d3c4b5a6f1e2d3c4", "--json"},
		&stdout,
		&stderr,
	)
	if code != ExitSuccess {
		t.Fatalf("exit code = %d: %s", code, stderr.String())
	}
	var response daemon.SessionReadResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	for _, chunk := range response.Chunks {
		output.WriteString(chunk.Data)
	}
	if got := output.String(); got != "safetext" || strings.ContainsRune(got, '\x1b') {
		t.Fatalf("JSON exposed unsafe chunk data %q", got)
	}
}
