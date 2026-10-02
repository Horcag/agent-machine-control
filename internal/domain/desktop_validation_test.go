package domain

import (
	"strings"
	"testing"
)

func TestDesktopProtocolBoundsAndIdentities(t *testing.T) {
	base := DesktopRequest{RequestID: strings.Repeat("a", 32), Deadline: "2026-10-02T20:00:00Z", Action: "windows"}
	if err := base.Validate(); err != nil || !base.ObserveOnly() {
		t.Fatalf("observation rejected: %v", err)
	}
	invalid := []DesktopRequest{
		{RequestID: "../other", Deadline: base.Deadline, Action: "status"},
		{RequestID: base.RequestID, Deadline: "bad", Action: "status"},
		{RequestID: base.RequestID, Deadline: base.Deadline, Action: "host.click"},
		{RequestID: base.RequestID, Deadline: base.Deadline, Action: "window.close"},
		{RequestID: base.RequestID, Deadline: base.Deadline, Action: "window.close", WindowID: "script"},
		{RequestID: base.RequestID, Deadline: base.Deadline, Action: "uia.invoke"},
		{RequestID: base.RequestID, Deadline: base.Deadline, Action: "launch"},
		{RequestID: base.RequestID, Deadline: base.Deadline, Action: "clipboard.set", Text: "x\x00"},
		{RequestID: base.RequestID, Deadline: base.Deadline, Action: "clipboard.set", Text: strings.Repeat("x", 65537)},
		{RequestID: base.RequestID, Deadline: base.Deadline, Action: "launch", Executable: "app.exe", Arguments: []string{"bad\x00"}},
		{RequestID: base.RequestID, Deadline: base.Deadline, Action: "status", ElementID: "bad\n"},
		{RequestID: base.RequestID, Deadline: base.Deadline, Action: "windows", WindowIdentity: "bad\n"},
		{RequestID: base.RequestID, Deadline: base.Deadline, Action: "scroll", Axis: "depth"},
		{RequestID: base.RequestID, Deadline: base.Deadline, Action: "scroll", X: 65536},
		{RequestID: base.RequestID, Deadline: base.Deadline, Action: "window.resize", WindowID: "42", Width: 0, Height: 100},
	}
	for i, req := range invalid {
		if req.Validate() == nil {
			t.Errorf("invalid request %d accepted", i)
		}
	}
	valid := base
	valid.Action = "window.resize"
	valid.WindowID = "42"
	valid.Width = 100
	valid.Height = 100
	if valid.Validate() != nil || valid.ObserveOnly() {
		t.Fatal("valid mutation rejected")
	}
}

func TestConsoleDoubleClickAndPointerModifierBounds(t *testing.T) {
	base := ConsoleInput{Kind: "click", FrameID: "observed", Button: "left", Count: 2, Modifiers: "ctrl+shift"}
	if base.Validate() != nil {
		t.Fatal("bounded double click rejected")
	}
	for _, modify := range []func(*ConsoleInput){func(in *ConsoleInput) { in.Count = 3 }, func(in *ConsoleInput) { in.Modifiers = "ctrl+ctrl" }, func(in *ConsoleInput) { in.Modifiers = "script" }, func(in *ConsoleInput) { in.Kind = "key"; in.Key = "enter" }, func(in *ConsoleInput) { in.Kind = "drag" }} {
		in := base
		modify(&in)
		if in.Validate() == nil {
			t.Fatal("invalid pointer gesture accepted")
		}
	}
}
