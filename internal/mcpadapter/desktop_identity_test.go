package mcpadapter

import (
	"encoding/json"
	"net/http"
	"reflect"
	"sync"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

const (
	desktopVMA = "local:c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	desktopVMB = "local:c4a523d4-6b99-4d62-a5e2-4752c0f20002"
)

type desktopIdentityCall struct {
	route, target, resolved string
}

// This daemon fixture resolves aliases at each request, so reusing "default"
// after the mutation would observe a different VM.
type desktopIdentityDaemon struct {
	mu        sync.Mutex
	defaultVM string
	captureVM string
	calls     []desktopIdentityCall
	semantic  domain.DesktopRequest
}

func (d *desktopIdentityDaemon) record(route, target string) {
	resolved := target
	if target == "default" {
		resolved = d.defaultVM
	}
	d.calls = append(d.calls, desktopIdentityCall{route, target, resolved})
}

func (d *desktopIdentityDaemon) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		var response any
		switch r.URL.Path {
		case "/v1/desktop/action":
			var req app.DesktopActionRequest
			decodeDesktopMCPRequest(t, r, &req)
			d.record(r.URL.Path, req.Target)
			response = d.desktopAction(req)
		case "/v1/console/input":
			var req app.ConsoleInputRequest
			decodeDesktopMCPRequest(t, r, &req)
			d.record(r.URL.Path, req.Target)
			d.defaultVM = desktopVMB
			response = domain.Receipt{ReceiptID: "synthetic-input", Target: domain.MachineRef(desktopVMA)}
		case "/v1/console/screenshot":
			var req app.ConsoleScreenshotRequest
			decodeDesktopMCPRequest(t, r, &req)
			d.record(r.URL.Path, req.Target)
			d.defaultVM = desktopVMB
			response = domain.ConsoleFrame{VMID: d.captureVM, FrameID: "synthetic-frame", MIMEType: "image/png", Data: []byte("synthetic-image")}
		case "/v1/desktop/lab/active":
			var req map[string]string
			decodeDesktopMCPRequest(t, r, &req)
			d.record(r.URL.Path, req["target"])
			response = app.ConsoleLabGrantStatus{State: "active", Grant: app.ConsoleLabGrant{GrantID: "captured-vm-grant"}}
		default:
			t.Errorf("unexpected identity route: %s", r.URL.Path)
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Error(err)
		}
	}
}

func (d *desktopIdentityDaemon) desktopAction(req app.DesktopActionRequest) app.DesktopActionResult {
	if req.Request.Action == "clipboard.set" {
		d.defaultVM = desktopVMB
		return app.DesktopActionResult{Receipt: &domain.Receipt{ReceiptID: "synthetic-action", Target: domain.MachineRef(desktopVMA)}}
	}
	d.semantic = req.Request
	return app.DesktopActionResult{Response: domain.DesktopResponse{Success: true}}
}

func (d *desktopIdentityDaemon) assertCalls(t *testing.T, want []desktopIdentityCall, action string) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if !reflect.DeepEqual(d.calls, want) {
		t.Fatalf("identity calls=%+v want=%+v", d.calls, want)
	}
	if d.defaultVM != desktopVMB || d.semantic.Action != action {
		t.Fatalf("default=%s semantic=%+v", d.defaultVM, d.semantic)
	}
}

func TestDesktopActObserveAfterPinsMutationReceiptTarget(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "semantic-action", true: "native-input"}[native], func(t *testing.T) {
			d := &desktopIdentityDaemon{defaultVM: desktopVMA, captureVM: desktopVMA}
			a := desktopMCPServer(t, d.handler(t))
			in := DesktopActInput{Target: "default", LabGrantID: "explicit-grant", Reason: "synthetic action", IdempotencyKey: "identity-key", Deadline: "2026-10-02T20:00:00Z", ObserveAfter: true}
			mutationRoute := "/v1/desktop/action"
			if native {
				in.Input = &domain.ConsoleInput{Kind: "key", Key: "enter"}
				mutationRoute = "/v1/console/input"
			} else {
				in.Action = &domain.DesktopRequest{Action: "clipboard.set", Text: "synthetic text"}
			}
			result, out, err := a.DesktopAct(t.Context(), nil, in)
			if err != nil || out.Result.Receipt == nil || string(out.Result.Receipt.Target) != desktopVMA {
				t.Fatalf("out=%+v err=%v", out, err)
			}
			assertDesktopMCPImage(t, result, "image/png", []byte("synthetic-image"))
			if out.Observation == nil || out.Observation.Frame.VMID != desktopVMA || !out.Observation.HelperAvailable || out.Observation.LabGrantID != "captured-vm-grant" {
				t.Fatalf("observation=%+v", out.Observation)
			}
			d.assertCalls(t, []desktopIdentityCall{
				{mutationRoute, "default", desktopVMA},
				{"/v1/console/screenshot", desktopVMA, desktopVMA},
				{"/v1/desktop/action", desktopVMA, desktopVMA},
				{"/v1/desktop/lab/active", desktopVMA, desktopVMA},
			}, "windows")
		})
	}
}

func TestDesktopObservePinsCapturedVMForWindowsAndTree(t *testing.T) {
	for _, action := range []string{"windows", "uia.tree"} {
		t.Run(action, func(t *testing.T) {
			d := &desktopIdentityDaemon{defaultVM: desktopVMA, captureVM: desktopVMA}
			a := desktopMCPServer(t, d.handler(t))
			in := DesktopObserveInput{Target: "default", Width: 8, Height: 4}
			if action == "uia.tree" {
				in.WindowID, in.WindowIdentity = "42", "synthetic-window"
			}
			result, out, err := a.DesktopObserve(t.Context(), nil, in)
			if err != nil || !out.HelperAvailable || out.Frame.VMID != desktopVMA {
				t.Fatalf("out=%+v err=%v", out, err)
			}
			assertDesktopMCPImage(t, result, "image/png", []byte("synthetic-image"))
			d.assertCalls(t, []desktopIdentityCall{
				{"/v1/console/screenshot", "default", desktopVMA},
				{"/v1/desktop/action", desktopVMA, desktopVMA},
				{"/v1/desktop/lab/active", desktopVMA, desktopVMA},
			}, action)
			d.mu.Lock()
			defer d.mu.Unlock()
			if d.semantic.WindowID != in.WindowID || d.semantic.WindowIdentity != in.WindowIdentity {
				t.Fatalf("semantic window=%+v", d.semantic)
			}
		})
	}
}
