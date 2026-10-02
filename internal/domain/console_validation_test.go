package domain

import (
	"strings"
	"testing"
)

func TestConsoleInputValidation(t *testing.T) {
	for _, input := range []ConsoleInput{{Kind: "key", Key: "ctrl+alt+delete"}, {Kind: "type", Text: "hello 世界"}, {Kind: "move", FrameID: "frame"}, {Kind: "click", FrameID: "frame", Button: "left"}, {Kind: "drag", FrameID: "frame", Button: "right", ToX: 2}} {
		if err := input.Validate(); err != nil {
			t.Fatalf("valid input rejected: %v", err)
		}
	}
	for _, input := range []ConsoleInput{{Kind: "unknown"}, {Kind: "type", Text: "\x00"}, {Kind: "type", Text: strings.Repeat("😀", 129)}, {Kind: "key", Key: "ctrl+ctrl+a"}, {Kind: "move", FrameID: "frame", ToX: 1}, {Kind: "type", Text: "ok", X: 1}} {
		if input.Validate() == nil {
			t.Fatal("invalid input accepted")
		}
	}
	codes, err := ConsoleKeyCodes("ctrl+alt+delete")
	if err != nil || len(codes) != 3 || codes[2] != 46 {
		t.Fatalf("chord: %v,%v", codes, err)
	}
}
