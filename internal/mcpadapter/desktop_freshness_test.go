package mcpadapter

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Synthetic elapsed time makes slow lookups deterministic without wall-clock sleeps.
type desktopFreshnessDaemon struct {
	now                   time.Time
	routes                []string
	captures              int
	helperFail, grantFail bool
	captureFailure        int
	finalVM               string
	finalFrame            domain.ConsoleFrame
	actions               []app.DesktopActionRequest
	inputs                []app.ConsoleInputRequest
}

func (d *desktopFreshnessDaemon) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		d.routes = append(d.routes, r.URL.Path)
		var response any
		switch r.URL.Path {
		case "/v1/console/screenshot":
			response = d.screenshot(t, w, r)
		case "/v1/desktop/action":
			var req app.DesktopActionRequest
			decodeDesktopMCPRequest(t, r, &req)
			if req.Request.Action == "clipboard.set" {
				d.actions = append(d.actions, req)
				response = app.DesktopActionResult{Receipt: &domain.Receipt{ReceiptID: "mutation-receipt", Target: domain.MachineRef(desktopVMA)}}
				break
			}
			if req.Target != desktopVMA {
				t.Errorf("helper target=%s", req.Target)
			}
			d.now = d.now.Add(20 * time.Second)
			if d.helperFail {
				http.Error(w, "helper unavailable", http.StatusNotImplemented)
				return
			}
			response = app.DesktopActionResult{Response: domain.DesktopResponse{Success: true}}
		case "/v1/console/input":
			var req app.ConsoleInputRequest
			decodeDesktopMCPRequest(t, r, &req)
			d.inputs = append(d.inputs, req)
			response = domain.Receipt{ReceiptID: "mutation-receipt", Target: domain.MachineRef(desktopVMA)}
		case "/v1/desktop/lab/active":
			var req map[string]string
			decodeDesktopMCPRequest(t, r, &req)
			if req["target"] != desktopVMA {
				t.Errorf("grant target=%s", req["target"])
			}
			d.now = d.now.Add(20 * time.Second)
			if d.grantFail {
				http.Error(w, "grant unavailable", http.StatusServiceUnavailable)
				return
			}
			response = app.ConsoleLabGrantStatus{State: "active", Grant: app.ConsoleLabGrant{GrantID: "synthetic-grant"}}
		default:
			t.Errorf("unexpected route=%s", r.URL.Path)
		}
		if response == nil {
			return
		}
		_ = json.NewEncoder(w).Encode(response)
	}
}

func (d *desktopFreshnessDaemon) screenshot(t *testing.T, w http.ResponseWriter, r *http.Request) any {
	t.Helper()
	var req app.ConsoleScreenshotRequest
	decodeDesktopMCPRequest(t, r, &req)
	d.captures++
	wantTarget := desktopVMA
	if d.captures == 1 && len(d.actions)+len(d.inputs) == 0 {
		wantTarget = "default"
	}
	if req.Target != wantTarget {
		t.Errorf("capture target=%s want=%s", req.Target, wantTarget)
	}
	if d.captures == d.captureFailure {
		http.Error(w, "capture unavailable", http.StatusServiceUnavailable)
		return nil
	}
	frame := domain.ConsoleFrame{VMID: desktopVMA, FrameID: "initial-frame", Data: []byte("initial-image"), MIMEType: "image/png", Width: req.Width, Height: req.Height, ObservedAt: d.now}
	if d.captures%2 == 0 {
		frame.FrameID, frame.Data, frame.SHA256 = "final-frame", []byte("final-image"), "final-digest"
		frame.NativeWidth, frame.NativeHeight = 1920, 1080
		if d.finalVM != "" {
			frame.VMID = d.finalVM
		}
		d.finalFrame = frame
	}
	return frame
}

func TestDesktopObserveReturnsFinalFrameAfterSlowLookups(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		helperFail, grantFail bool
	}{
		{name: "slow-helper-and-grant"}, {name: "helper-unavailable", helperFail: true},
		{name: "grant-unavailable", grantFail: true}, {name: "both-unavailable", helperFail: true, grantFail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
			d := &desktopFreshnessDaemon{now: start, helperFail: tc.helperFail, grantFail: tc.grantFail}
			a := desktopMCPServer(t, d.handler(t))
			result := callDesktopMCPTool(t, a, "desktop_observe", map[string]any{"target": "default", "width": 8, "height": 4})
			assertDesktopMCPImage(t, result, "image/png", []byte("final-image"))
			data, err := json.Marshal(result.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			var out DesktopObserveResult
			if err := json.Unmarshal(data, &out); err != nil {
				t.Fatal(err)
			}
			want := ConsoleFrameMetadata{VMID: d.finalFrame.VMID, FrameID: d.finalFrame.FrameID, Width: 8, Height: 4, NativeWidth: 1920, NativeHeight: 1080, MIMEType: "image/png", SHA256: "final-digest", ObservedAt: start.Add(40 * time.Second)}
			if out.Frame != want || out.HelperAvailable != !tc.helperFail || (out.LabGrantID != "") != !tc.grantFail {
				t.Fatalf("out=%+v want frame=%+v", out, want)
			}
			if !reflect.DeepEqual(d.routes, []string{"/v1/console/screenshot", "/v1/desktop/action", "/v1/desktop/lab/active", "/v1/console/screenshot"}) {
				t.Fatalf("routes=%v", d.routes)
			}
		})
	}
}

func TestDesktopObserveCaptureFailuresReturnNoStaleFrame(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure int
		finalVM string
		calls   int
	}{
		{name: "initial-capture-error", failure: 1, calls: 1},
		{name: "final-capture-error", failure: 2, calls: 4},
		{name: "final-VM-mismatch", finalVM: desktopVMB, calls: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &desktopFreshnessDaemon{captureFailure: tc.failure, finalVM: tc.finalVM}
			a := desktopMCPServer(t, d.handler(t))
			result, out, err := a.DesktopObserve(t.Context(), nil, DesktopObserveInput{Target: "default"})
			if err != nil || result == nil || !result.IsError || out.Frame != (ConsoleFrameMetadata{}) {
				t.Fatalf("result=%+v out=%+v err=%v", result, out, err)
			}
			for _, content := range result.Content {
				if _, ok := content.(*mcp.ImageContent); ok {
					t.Fatal("failed capture returned stale image")
				}
			}
			if len(d.routes) != tc.calls {
				t.Fatalf("routes=%v", d.routes)
			}
		})
	}
}

func TestDesktopActFailedFinalObservationPreservesReceiptAndRetry(t *testing.T) {
	for _, tc := range []struct {
		name             string
		native, mismatch bool
	}{
		{name: "semantic-capture-error"}, {name: "semantic-mismatch", mismatch: true},
		{name: "native-capture-error", native: true}, {name: "native-mismatch", native: true, mismatch: true},
	} {
		t.Run(tc.name, func(t *testing.T) { testDesktopFailedObservationRetry(t, tc.native, tc.mismatch) })
	}
}

func testDesktopFailedObservationRetry(t *testing.T, native, mismatch bool) {
	t.Helper()
	d := &desktopFreshnessDaemon{}
	if mismatch {
		d.finalVM = desktopVMB
	}
	a := desktopMCPServer(t, d.handler(t))
	in := DesktopActInput{Target: "default", LabGrantID: "explicit-grant", Reason: "synthetic mutation", IdempotencyKey: "retry-key", Deadline: "2026-10-02T20:00:00Z", ObserveAfter: true}
	if native {
		in.Input = &domain.ConsoleInput{Kind: "key", Key: "enter"}
	} else {
		in.Action = &domain.DesktopRequest{Action: "clipboard.set", Text: "synthetic text"}
	}
	for attempt := range 2 {
		if !mismatch {
			d.captureFailure = 2 * (attempt + 1)
		}
		result, out, err := a.DesktopAct(t.Context(), nil, in)
		assertDesktopFailedObservationReceipt(t, result, out, err)
		if len(d.actions)+len(d.inputs) != attempt+1 {
			t.Fatal("observation failure replayed mutation")
		}
	}
	if native {
		if len(d.inputs) != 2 || !reflect.DeepEqual(d.inputs[0], d.inputs[1]) {
			t.Fatalf("inputs=%+v", d.inputs)
		}
	} else if len(d.actions) != 2 || !reflect.DeepEqual(d.actions[0], d.actions[1]) {
		t.Fatalf("actions=%+v", d.actions)
	}
}

func assertDesktopFailedObservationReceipt(t *testing.T, result *mcp.CallToolResult, out DesktopActResult, err error) {
	t.Helper()
	if err != nil || result != nil || out.Observation != nil || out.Result.Receipt == nil || out.Result.Receipt.ReceiptID != "mutation-receipt" || string(out.Result.Receipt.Target) != desktopVMA {
		t.Fatalf("result=%+v out=%+v err=%v", result, out, err)
	}
}
