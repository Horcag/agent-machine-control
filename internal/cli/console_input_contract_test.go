package cli

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestConsoleScrollRejectedBeforeBackendDispatch(t *testing.T) {
	for _, direct := range []bool{false, true} {
		name := "daemon"
		if direct {
			name = "direct"
		}
		t.Run(name, func(t *testing.T) {
			a := desktopCLIHTTP(t, func(http.ResponseWriter, *http.Request) { t.Error("unsupported console input reached daemon") })
			svc := &consoleStub{}
			WithConsoleService(svc)(a)
			for _, tc := range []struct {
				name       string
				args       []string
				diagnostic string
			}{
				{"scroll", []string{"scroll", "synthetic-vm", "--delta", "120"}, "use guest desktop scrolling via amc desktop action"},
				{"delta", []string{"move", "--frame-id", "synthetic-frame", "--delta", "120"}, "flag provided but not defined: -delta"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					args := append([]string{"console"}, tc.args...)
					if direct {
						args = append([]string{"--direct"}, args...)
					}
					var stdout, stderr bytes.Buffer
					if code := a.Run(args, &stdout, &stderr); code != ExitUsage || svc.calls != 0 || stdout.Len() != 0 || !strings.Contains(stderr.String(), tc.diagnostic) {
						t.Fatalf("code=%d calls=%d stdout=%s stderr=%s", code, svc.calls, stdout.String(), stderr.String())
					}
				})
			}
		})
	}
}

func TestConsoleSupportedInputCommandsPreserveRequests(t *testing.T) {
	for _, tc := range []struct {
		input domain.ConsoleInput
		flags []string
	}{
		{domain.ConsoleInput{Kind: "key", Key: "ctrl+alt+delete"}, []string{"--key", "ctrl+alt+delete"}},
		{domain.ConsoleInput{Kind: "type", Text: "synthetic text"}, []string{"--text", "synthetic text"}},
		{domain.ConsoleInput{Kind: "move", FrameID: "synthetic-frame", X: 12, Y: 34, Modifiers: "ctrl"}, []string{"--frame-id", "synthetic-frame", "--x", "12", "--y", "34", "--modifiers", "ctrl"}},
		{domain.ConsoleInput{Kind: "click", FrameID: "synthetic-frame", X: 12, Y: 34, Button: "right", Count: 2}, []string{"--frame-id", "synthetic-frame", "--x", "12", "--y", "34", "--button", "right", "--count", "2"}},
		{domain.ConsoleInput{Kind: "drag", FrameID: "synthetic-frame", X: 12, Y: 34, ToX: 56, ToY: 78, Button: "left"}, []string{"--frame-id", "synthetic-frame", "--x", "12", "--y", "34", "--to-x", "56", "--to-y", "78", "--button", "left"}},
	} {
		t.Run(tc.input.Kind, func(t *testing.T) {
			svc := &consoleStub{}
			a := NewApp(nil, WithConsoleService(svc))
			args := append([]string{"--direct", "console", tc.input.Kind, "synthetic-vm"}, tc.flags...)
			args = append(args, "--reason", "synthetic interaction", "--idempotency-key", "synthetic-key", "--approval-id", "synthetic-approval", "--deadline", "2026-10-02T20:00:00Z", "--lab-grant-id", "synthetic-grant")
			var stdout, stderr bytes.Buffer
			code := a.Run(args, &stdout, &stderr)
			if code != ExitSuccess || svc.calls != 1 || svc.input.Input != tc.input || svc.input.Input.Validate() != nil {
				t.Fatalf("code=%d calls=%d input=%+v stderr=%s", code, svc.calls, svc.input.Input, stderr.String())
			}
			if svc.input.Target != "synthetic-vm" || svc.input.Reason != "synthetic interaction" || svc.input.IdempotencyKey != "synthetic-key" || svc.input.Deadline != "2026-10-02T20:00:00Z" || svc.input.LabGrantID != "synthetic-grant" || svc.input.ApprovalID != "synthetic-approval" {
				t.Fatalf("mutation metadata changed: %+v", svc.input)
			}
		})
	}
}
