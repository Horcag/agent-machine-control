package desktop

import (
	"strings"
	"testing"
	"time"
)

func TestWindowAndUIAActionsRequireExactBoundedTargets(t *testing.T) {
	for _, action := range []string{"window.focus", "window.close", "window.minimize", "window.maximize", "window.restore", "window.move", "window.resize", "scroll", "uia.tree", "uia.invoke", "uia.setvalue", "uia.select", "uia.toggle", "uia.expand", "uia.collapse", "uia.scroll"} {
		req := request(action)
		req.WindowID = "123"
		req.WindowIdentity = "123:456:638000000000000000"
		switch action {
		case "window.resize":
			req.Width, req.Height = 800, 600
		case "scroll":
			req.Delta = -120
		case "uia.invoke", "uia.setvalue", "uia.select", "uia.toggle", "uia.expand", "uia.collapse", "uia.scroll":
			req.ElementID = "42:1:123"
			if action == "uia.scroll" {
				req.Delta = -1
			}
		}
		if err := validateRequest(req, time.Now()); err != nil {
			t.Fatalf("valid %s: %v", action, err)
		}
		req.WindowID = "123;evil"
		if validateRequest(req, time.Now()) == nil {
			t.Fatalf("invalid window %s", action)
		}
	}
	for _, id := range []string{"", "0", "-1", "12345678901234567890", "9223372036854775808"} {
		if windowIDValid(id) {
			t.Fatalf("invalid HWND %s", id)
		}
	}
	for _, id := range []string{"", "1:abc", "1:2147483648", strings.Repeat("1", 257)} {
		if elementIDValid(id) {
			t.Fatalf("invalid element ID %s", id)
		}
	}
}

func TestLaunchRequiresAbsoluteExecutableAndBoundedArguments(t *testing.T) {
	req := request("launch")
	req.Executable = "C:\\Windows\\System32\\notepad.exe"
	req.Arguments = []string{`C:\synthetic folder\file.txt`, `quote"and\slash`}
	if validateRequest(req, time.Now()) != nil {
		t.Fatal("valid application rejected")
	}
	for _, path := range []string{"notepad.exe", `\\server\app.exe`, `C:\script.ps1`, `C:\app.exe\n`, "C:\\app\".exe"} {
		req.Executable = path
		if validateRequest(req, time.Now()) == nil {
			t.Fatalf("unsafe path %s", path)
		}
	}
	req.Executable = "C:\\Windows\\notepad.exe"
	req.Arguments = []string{strings.Repeat("a", 1025)}
	if validateRequest(req, time.Now()) == nil {
		t.Fatal("oversized argument accepted")
	}
}

func TestScrollAxesAndActionFieldsAreBounded(t *testing.T) {
	for _, action := range []string{"scroll", "uia.scroll"} {
		for _, axis := range []string{"", "vertical", "horizontal"} {
			req := request(action)
			req.WindowID = "123"
			req.WindowIdentity = "123:456:638000000000000000"
			req.Axis = axis
			req.Delta = 1
			if action == "uia.scroll" {
				req.ElementID = "42:1:123"
			}
			if validateRequest(req, time.Now()) != nil {
				t.Fatalf("valid %s %s", action, axis)
			}
			req.Axis = "diagonal"
			if validateRequest(req, time.Now()) == nil {
				t.Fatal("invalid wheel axis accepted")
			}
			req.Axis = axis
			req.Delta = 1201
			if validateRequest(req, time.Now()) == nil {
				t.Fatal("oversized scroll accepted")
			}
			req.Delta = 0
			if validateRequest(req, time.Now()) == nil {
				t.Fatal("zero scroll accepted")
			}
		}
	}
}

func TestUnrelatedAxesAndTextBeyondGuestLimitAreRejected(t *testing.T) {
	req := request("status")
	req.Axis = "vertical"
	if validateRequest(req, time.Now()) == nil {
		t.Fatal("unrelated axis accepted")
	}
	req = request("clipboard.set")
	req.Text = strings.Repeat("a", 4097)
	if validateRequest(req, time.Now()) == nil {
		t.Fatal("guest text limit bypassed")
	}
	req.Text = strings.Repeat("😀", 2049)
	if validateRequest(req, time.Now()) == nil {
		t.Fatal("UTF16 guest text limit bypassed")
	}
}

func TestWindowBindingRejectsMissingOrReusedIdentity(t *testing.T) {
	req := request("window.focus")
	req.WindowID = "123"
	for _, id := range []string{"", "124:456:638000000000000000", "123:0:638000000000000000", "123:456:no", "123:456:1:2"} {
		req.WindowIdentity = id
		if validateRequest(req, time.Now()) == nil {
			t.Fatalf("unsafe identity %s", id)
		}
	}
	req.Action = "uia.tree"
	req.WindowIdentity = ""
	if validateRequest(req, time.Now()) != nil {
		t.Fatal("unbound observation rejected")
	}
}
