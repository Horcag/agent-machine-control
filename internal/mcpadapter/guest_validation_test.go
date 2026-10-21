package mcpadapter

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestGuestMCPMalformedShapesNeverDispatch(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	a := &Adapter{client: client.New(server.URL, "synthetic")}
	for _, tc := range []struct {
		operation string
		mutate    func(*GuestInput)
	}{
		{"prepare", func(in *GuestInput) { in.MaxOutput = 64 }},
		{"prepare", func(in *GuestInput) { in.ArtifactIDs = []string{strings.Repeat("a", 32)} }},
		{"put", func(in *GuestInput) { in.MaxOutput = 1 }},
		{"put", func(in *GuestInput) { in.ArtifactIDs = []string{strings.Repeat("a", 32)} }},
		{"get", func(in *GuestInput) { in.ArtifactIDs = nil }},
		{"get", func(in *GuestInput) { in.Source = "private-secret-path" }},
		{"exec", func(in *GuestInput) { in.MaxOutput = 16*1024*1024 + 1 }},
		{"exec", func(in *GuestInput) { in.ArtifactIDs = []string{strings.Repeat("a", 32)} }},
		{"remove-owned", func(in *GuestInput) { in.ArtifactIDs = nil }},
		{"remove-owned", func(in *GuestInput) { in.MaxOutput = 1 }},
	} {
		in := guestMCPCaseInput(tc.operation, "private-secret-path", t.TempDir())
		tc.mutate(&in)
		result, _, err := callGuestMCP(a, t, in, tc.operation)
		if err != nil || result == nil || !result.IsError {
			t.Fatalf("%s: %+v %v", tc.operation, result, err)
		}
		text := result.Content[0].(*mcp.TextContent).Text
		if !strings.Contains(text, "invalid_argument") || !strings.Contains(text, "guest_") || strings.Contains(text, "private-secret") {
			t.Fatalf("unsafe or unhelpful error: %q", text)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid requests dispatched %d times", calls)
	}
}

func TestGuestMCPTypedDenialAcrossTransports(t *testing.T) {
	for _, category := range []string{"approval_required", "guest_workspace_unprovisioned", "invalid_argument", "guest_uncertain"} {
		t.Run(category, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if _, _, err := daemon.DecodeGuestMetadata(r.Body); err != nil {
					t.Error(err)
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Trailer", daemon.GuestResultTrailer)
				data, _ := json.Marshal(daemon.GuestFinal{Category: category, Error: "private-secret-path-token"})
				w.Header().Set(daemon.GuestResultTrailer, base64.RawStdEncoding.EncodeToString(data))
			}))
			defer server.Close()
			a := &Adapter{client: client.New(server.URL, "synthetic")}
			cs := recordingMCPClient(t, a)
			result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "guest_prepare", Arguments: guestMCPCaseInput("prepare", "", t.TempDir())})
			if err != nil || result == nil || !result.IsError || calls != 1 {
				t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
			}
			text := result.Content[0].(*mcp.TextContent).Text
			if !strings.Contains(text, category) || strings.Contains(text, "private") {
				t.Fatalf("category lost or secret exposed: %q", text)
			}
		})
	}
}

func TestGuestMCPSchemasRejectIrrelevantFieldsBeforeHTTP(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	cs := recordingMCPClient(t, &Adapter{client: client.New(server.URL, "synthetic")})
	for _, tc := range []struct {
		operation, field string
		value            any
	}{
		{"prepare", "source", "private-secret-path"}, {"prepare", "max_output", 64},
		{"put", "destination", "private-secret-path"}, {"get", "source", "private-secret-path"},
		{"exec", "artifact_ids", []string{strings.Repeat("a", 32)}}, {"remove-owned", "destination", "private-secret-path"},
	} {
		in := guestMCPCaseInput(tc.operation, "private-secret-path", t.TempDir())
		encoded, _ := json.Marshal(in)
		var arguments map[string]any
		if err := json.Unmarshal(encoded, &arguments); err != nil {
			t.Fatal(err)
		}
		arguments[tc.field] = tc.value
		result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "guest_" + strings.ReplaceAll(tc.operation, "-", "_"), Arguments: arguments})
		if err == nil && (result == nil || !result.IsError) {
			t.Fatalf("schema admitted %s %s: %+v", tc.operation, tc.field, result)
		}
	}
	if calls != 0 {
		t.Fatalf("malformed schema input reached HTTP %d times", calls)
	}
}
