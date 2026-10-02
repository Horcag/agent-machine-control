package mcpadapter

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color/palette"
	"image/gif"
	"image/png"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func desktopMCPServer(t *testing.T, handler http.HandlerFunc) *Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer synthetic-agent-token" {
			t.Error("MCP caller authentication changed")
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return &Adapter{client: client.New(srv.URL, "synthetic-agent-token")}
}

func TestDesktopObserveMCPTransportAndHelperRecovery(t *testing.T) {
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 8, 4))); err != nil {
		t.Fatal(err)
	}
	for _, helperAvailable := range []bool{true, false} {
		t.Run(map[bool]string{true: "helper", false: "native-recovery"}[helperAvailable], func(t *testing.T) {
			var routes []string
			expiry := time.Date(2026, 10, 2, 20, 0, 0, 0, time.UTC)
			a := desktopMCPServer(t, func(w http.ResponseWriter, r *http.Request) {
				routes = append(routes, r.URL.Path)
				var response any
				switch r.URL.Path {
				case "/v1/console/screenshot":
					var req app.ConsoleScreenshotRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Error(err)
					}
					if req.Target != "default" || req.Width != 8 || req.Height != 4 {
						t.Errorf("capture request=%+v", req)
					}
					response = domain.ConsoleFrame{VMID: "local:c4a523d4-6b99-4d62-a5e2-4752c0f20001", Data: pngBytes.Bytes(), MIMEType: "image/png", FrameID: "synthetic-frame", Width: 8, Height: 4}
				case "/v1/desktop/action":
					var req app.DesktopActionRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Error(err)
					}
					if req.Target != "local:c4a523d4-6b99-4d62-a5e2-4752c0f20001" || req.Request.Validate() != nil || req.Request.Action != "uia.tree" || req.Request.WindowID != "42" || req.Request.WindowIdentity != "synthetic-window" {
						t.Errorf("desktop request=%+v", req)
					}
					if !helperAvailable {
						http.Error(w, "helper disconnected", http.StatusNotImplemented)
						return
					}
					response = app.DesktopActionResult{Response: domain.DesktopResponse{Success: true, Windows: []domain.DesktopWindow{{ID: "42", Identity: "synthetic-window", Title: "Synthetic editor"}}, Cursor: &domain.DesktopCursor{X: 123, Y: 456, Visible: true}}}
				case "/v1/desktop/lab/active":
					var req map[string]string
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Error(err)
					}
					if req["target"] != "local:c4a523d4-6b99-4d62-a5e2-4752c0f20001" {
						t.Error("grant target changed")
					}
					response = app.ConsoleLabGrantStatus{State: "active", Grant: app.ConsoleLabGrant{GrantID: "own-grant", Beneficiary: "agent:mcp-local", ExpiresAt: expiry}}
				default:
					t.Errorf("unexpected route %s", r.URL.Path)
				}
				if err := json.NewEncoder(w).Encode(response); err != nil {
					t.Error(err)
				}
			})
			ct, st := mcp.NewInMemoryTransports()
			ss, err := a.BuildServer().Connect(t.Context(), st, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer ss.Close()
			cs, err := mcp.NewClient(&mcp.Implementation{Name: "desktop-test", Version: "1"}, nil).Connect(t.Context(), ct, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer cs.Close()
			result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "desktop_observe", Arguments: map[string]any{"target": "default", "width": 8, "height": 4, "window_id": "42", "window_identity": "synthetic-window"}})
			if err != nil || result == nil || result.IsError || len(result.Content) != 1 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			img, ok := result.Content[0].(*mcp.ImageContent)
			if !ok || img.MIMEType != "image/png" || !bytes.Equal(img.Data, pngBytes.Bytes()) {
				t.Fatal("PNG lost through desktop MCP transport")
			}
			data, err := json.Marshal(result.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			var got DesktopObserveResult
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(data, []byte(`"data"`)) || got.Frame.FrameID != "synthetic-frame" || got.HelperAvailable != helperAvailable || got.LabGrantID != "own-grant" || !got.LabGrantExpiresAt.Equal(expiry) || !strings.Contains(got.CoordinateSpace, "native screen pixels") {
				t.Fatalf("structured=%s", data)
			}
			if helperAvailable && (got.Cursor == nil || got.Cursor.X != 123 || len(got.Desktop.Windows) != 1 || got.Desktop.Windows[0].Title != "Synthetic editor") {
				t.Fatalf("helper metadata=%+v", got)
			}
			if !helperAvailable && (got.Cursor != nil || got.Desktop.Success) {
				t.Fatal("unavailable helper reported data")
			}
			if !reflect.DeepEqual(routes, []string{"/v1/console/screenshot", "/v1/desktop/action", "/v1/desktop/lab/active"}) {
				t.Fatalf("routes=%v", routes)
			}
		})
	}
}

func TestDesktopActExactPayloadRetryAndGrantDiscovery(t *testing.T) {
	deadline := "2026-10-02T20:00:00Z"
	var requests []app.DesktopActionRequest
	var inputs []app.ConsoleInputRequest
	activeCalls := 0
	a := desktopMCPServer(t, func(w http.ResponseWriter, r *http.Request) {
		var response any
		switch r.URL.Path {
		case "/v1/desktop/lab/active":
			activeCalls++
			response = app.ConsoleLabGrantStatus{State: "active", Grant: app.ConsoleLabGrant{GrantID: "own-grant", Beneficiary: "agent:mcp-local"}}
		case "/v1/desktop/action":
			var req app.DesktopActionRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			requests = append(requests, req)
			response = app.DesktopActionResult{Response: domain.DesktopResponse{Success: true, RequestID: req.Request.RequestID}, Receipt: &domain.Receipt{ReceiptID: "synthetic-action-receipt"}}
		case "/v1/console/input":
			var req app.ConsoleInputRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			inputs = append(inputs, req)
			response = domain.Receipt{ReceiptID: "synthetic-input-receipt"}
		default:
			t.Errorf("unexpected route=%s", r.URL.Path)
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Error(err)
		}
	})
	action := domain.DesktopRequest{Action: "clipboard.set", Text: "synthetic text"}
	in := DesktopActInput{Target: "default", Action: &action, Reason: "synthetic action", IdempotencyKey: "exact-action-key", Deadline: deadline}
	for range 2 {
		result, out, err := a.DesktopAct(t.Context(), nil, in)
		if err != nil || result != nil || out.Result.Receipt == nil || out.Result.Receipt.ReceiptID != "synthetic-action-receipt" {
			t.Fatalf("result=%+v out=%+v err=%v", result, out, err)
		}
	}
	if activeCalls != 2 || len(requests) != 2 || !reflect.DeepEqual(requests[0], requests[1]) || requests[0].Request.Validate() != nil || requests[0].Request.Deadline != deadline || requests[0].LabGrantID != "own-grant" || requests[0].IdempotencyKey != in.IdempotencyKey || requests[0].Reason != in.Reason || requests[0].Target != in.Target || action.RequestID != "" || action.Deadline != "" {
		t.Fatalf("requests=%+v action=%+v active=%d", requests, action, activeCalls)
	}
	input := domain.ConsoleInput{Kind: "key", Key: "enter"}
	in.Action = nil
	in.Input = &input
	in.LabGrantID = "explicit-grant"
	in.IdempotencyKey = "input-key"
	result, out, err := a.DesktopAct(t.Context(), nil, in)
	want := app.ConsoleInputRequest{Target: in.Target, Input: input, LabGrantID: in.LabGrantID, Reason: in.Reason, IdempotencyKey: in.IdempotencyKey, Deadline: in.Deadline}
	if err != nil || result != nil || out.Result.Receipt == nil || out.Result.Receipt.ReceiptID != "synthetic-input-receipt" || len(inputs) != 1 || inputs[0] != want || activeCalls != 2 {
		t.Fatalf("inputs=%+v result=%+v out=%+v err=%v", inputs, result, out, err)
	}
}

func TestDesktopActRejectsAmbiguityAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name        string
		in          DesktopActInput
		activeState string
		failPath    string
	}{
		{name: "none"},
		{name: "both", in: DesktopActInput{Action: &domain.DesktopRequest{}, Input: &domain.ConsoleInput{}}},
		{name: "mismatched-deadline", in: DesktopActInput{LabGrantID: "own-grant", Deadline: "2026-10-02T20:00:00Z", Action: &domain.DesktopRequest{Deadline: "2026-10-02T21:00:00Z"}}},
		{name: "disabled", in: DesktopActInput{Input: &domain.ConsoleInput{}}, activeState: "disabled"},
		{name: "grant-error", in: DesktopActInput{Input: &domain.ConsoleInput{}}, failPath: "/v1/desktop/lab/active"},
		{name: "action-error", in: DesktopActInput{LabGrantID: "own-grant", Action: &domain.DesktopRequest{}}, failPath: "/v1/desktop/action"},
		{name: "input-error", in: DesktopActInput{LabGrantID: "own-grant", Input: &domain.ConsoleInput{}}, failPath: "/v1/console/input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			a := desktopMCPServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path == tc.failPath {
					http.Error(w, "denied", http.StatusForbidden)
					return
				}
				if r.URL.Path != "/v1/desktop/lab/active" {
					t.Error("invalid payload reached mutation")
				}
				_ = json.NewEncoder(w).Encode(app.ConsoleLabGrantStatus{State: tc.activeState})
			})
			result, _, err := a.DesktopAct(t.Context(), nil, tc.in)
			if err != nil || result == nil || !result.IsError {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if (tc.name == "none" || tc.name == "both" || tc.name == "mismatched-deadline") && calls != 0 {
				t.Fatalf("invalid request made %d HTTP calls", calls)
			}
		})
	}
	a := NewAdapter(t.TempDir())
	result, _, err := a.DesktopAct(t.Context(), nil, DesktopActInput{Input: &domain.ConsoleInput{}})
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("missing daemon result=%+v err=%v", result, err)
	}
	result, _, err = a.DesktopObserve(t.Context(), nil, DesktopObserveInput{})
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("missing capture result=%+v err=%v", result, err)
	}
}

func TestConsoleRecordMCPTransportSeparatesGIFAndMetadata(t *testing.T) {
	var data bytes.Buffer
	img := image.NewPaletted(image.Rect(0, 0, 8, 4), palette.Plan9)
	if err := gif.EncodeAll(&data, &gif.GIF{Image: []*image.Paletted{img, img}, Delay: []int{10, 10}}); err != nil {
		t.Fatal(err)
	}
	req := app.ConsoleRecordRequest{Target: "default", Width: 8, Height: 4, Frames: 2, IntervalMillis: 100}
	observed := []time.Time{time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC), time.Date(2026, 10, 2, 10, 0, 0, 100000000, time.UTC)}
	a := desktopMCPServer(t, func(w http.ResponseWriter, r *http.Request) {
		var got app.ConsoleRecordRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/v1/console/record" || got != req {
			t.Errorf("record request=%+v route=%s", got, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(app.ConsoleRecording{Data: data.Bytes(), MIMEType: "image/gif", SHA256: "synthetic-digest", Width: 8, Height: 4, ObservedAt: observed})
	})
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
	result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "console_record", Arguments: req})
	if err != nil || result == nil || result.IsError || len(result.Content) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	content, ok := result.Content[0].(*mcp.ImageContent)
	if !ok || content.MIMEType != "image/gif" || !bytes.Equal(content.Data, data.Bytes()) {
		t.Fatal("GIF changed in MCP transport")
	}
	structured, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var metadata app.ConsoleRecording
	if err := json.Unmarshal(structured, &metadata); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(structured, []byte(`"data"`)) || metadata.SHA256 != "synthetic-digest" || metadata.Width != 8 || metadata.Height != 4 || !reflect.DeepEqual(metadata.ObservedAt, observed) {
		t.Fatalf("metadata=%s", structured)
	}
}

func TestConsoleRecordMCPDaemonErrors(t *testing.T) {
	adapters := []*Adapter{NewAdapter(t.TempDir()), desktopMCPServer(t, func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "unavailable", http.StatusNotImplemented) })}
	for _, a := range adapters {
		result, _, err := a.ConsoleRecord(t.Context(), nil, app.ConsoleRecordRequest{})
		if err != nil || result == nil || !result.IsError {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
}
