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

func TestConsoleFunctionKeys(t *testing.T) {
	for _, test := range []struct {
		key  string
		code uint32
	}{{"F1", 0x70}, {"F5", 0x74}, {"ctrl+F24", 0x87}} {
		codes, err := ConsoleKeyCodes(test.key)
		if err != nil || codes[len(codes)-1] != test.code {
			t.Fatalf("%s: codes=%v error=%v", test.key, codes, err)
		}
	}
	for _, key := range []string{"F0", "F25", "F01", "F+1", "F１", "F-1", "F999"} {
		if (ConsoleInput{Kind: "key", Key: key}).Validate() == nil {
			t.Fatalf("accepted invalid function key %s", key)
		}
	}
}

func TestConsoleDragDurationValidation(t *testing.T) {
	for _, duration := range []int{0, 20, 401, 5000} {
		input := ConsoleInput{Kind: "drag", FrameID: "frame", Button: "left", DurationMS: duration}
		if err := input.Validate(); err != nil {
			t.Fatalf("duration %d rejected: %v", duration, err)
		}
	}
	for _, duration := range []int{-1, 1, 19, 5001} {
		if (ConsoleInput{Kind: "drag", FrameID: "frame", Button: "left", DurationMS: duration}).Validate() == nil {
			t.Fatalf("unbounded duration %d accepted", duration)
		}
	}
	for _, input := range []ConsoleInput{{Kind: "move", FrameID: "frame"}, {Kind: "click", FrameID: "frame", Button: "left"}, {Kind: "key", Key: "enter"}, {Kind: "type", Text: "sample"}} {
		input.DurationMS = 400
		if input.Validate() == nil {
			t.Fatalf("duration accepted for %s", input.Kind)
		}
	}
	first := ConsoleInput{Kind: "drag", FrameID: "frame", Button: "left", DurationMS: 400}
	second := first
	second.DurationMS = 5000
	if ConsoleInputParameters(first)["input_sha256"] == ConsoleInputParameters(second)["input_sha256"] {
		t.Fatal("changed duration reused authorization/idempotency fingerprint")
	}
}
