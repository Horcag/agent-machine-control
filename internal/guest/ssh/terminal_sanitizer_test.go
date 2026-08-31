package ssh_test

import (
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

func TestStreamSanitizer_StripsTerminalControlsAtEveryBoundary(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "DCS", input: "before\x1bP1;2|device-data\x1b\\after", want: "beforeafter"},
		{name: "APC", input: "before\x1b_private-data\x1b\\after", want: "beforeafter"},
		{name: "PM", input: "before\x1b^private-data\x1b\\after", want: "beforeafter"},
		{name: "SOS", input: "before\x1bXprivate-data\x1b\\after", want: "beforeafter"},
		{name: "OSC BEL", input: "before\x1b]2;forged title\aafter", want: "beforeafter"},
		{name: "OSC ST", input: "before\x1b]52;c;clipboard\x1b\\after", want: "beforeafter"},
		{name: "8-bit CSI", input: "before\x9b31mafter", want: "beforeafter"},
		{name: "UTF-8 C1 CSI", input: "before\u009b31mafter", want: "beforeafter"},
		{name: "cursor movement", input: "before\x1b[20Aafter", want: "beforeafter"},
		{name: "alternate screen", input: "before\x1b[?1049hafter", want: "beforeafter"},
		{name: "bracketed paste", input: "before\x1b[?2004hafter", want: "beforeafter"},
		{name: "mouse mode", input: "before\x1b[?1000;1006hafter", want: "beforeafter"},
		{name: "device query", input: "before\x1b[6nafter", want: "beforeafter"},
		{name: "device attributes", input: "before\x1b[cafter", want: "beforeafter"},
		{name: "erase display", input: "before\x1b[2Jafter", want: "beforeafter"},
		{name: "scroll", input: "before\x1b[3Safter", want: "beforeafter"},
		{name: "window command", input: "before\x1b[8;40;120tafter", want: "beforeafter"},
		{name: "SGR stripped", input: "before\x1b[1;31mstyled\x1b[0mafter", want: "beforestyledafter"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for split := 0; split <= len(tt.input); split++ {
				if got := sanitizeAtBoundary(tt.input, split, ssh.SanitizerConfig{}); got != tt.want {
					t.Fatalf("split %d: got %q, want %q", split, got, tt.want)
				}
			}
		})
	}
}

func TestStreamSanitizer_UnterminatedControlsFailClosed(t *testing.T) {
	prefixes := []string{
		"safe\x1b[31",
		"safe\x1b]52;c;clipboard",
		"safe\x1bPdevice-data",
		"safe\x1b_private-data",
		"safe\x1b^private-data",
		"safe\x1bXprivate-data",
	}
	for _, input := range prefixes {
		sanitizer := ssh.NewStreamSanitizer(ssh.SanitizerConfig{})
		got := sanitizer.Push([]byte(input)) + sanitizer.Flush()
		if got != "safe" {
			t.Fatalf("unterminated control was not dropped: input=%q output=%q", input, got)
		}
	}
}

func TestStreamSanitizer_MalformedControlsFailClosed(t *testing.T) {
	inputs := []string{
		"safe\x1b[31\x01payload",
		"safe\x1b(☃payload",
		"safe\x1b[31\u0090payload",
	}
	for _, input := range inputs {
		sanitizer := ssh.NewStreamSanitizer(ssh.SanitizerConfig{})
		got := sanitizer.Push([]byte(input)) + sanitizer.Flush()
		if got != "safe" {
			t.Fatalf("malformed control did not fail closed: input=%q output=%q", input, got)
		}
	}
}

func TestStreamSanitizer_PreservesPlainTextAndWhitespace(t *testing.T) {
	input := "plain Привет 🔒\tcolumn\r\nnext line"
	for split := 0; split <= len(input); split++ {
		if got := sanitizeAtBoundary(input, split, ssh.SanitizerConfig{}); got != input {
			t.Fatalf("plain text changed at split %d: %q", split, got)
		}
	}
}

func TestStreamSanitizer_MultiMegabyteControlPayloadIsBounded(t *testing.T) {
	sanitizer := ssh.NewStreamSanitizer(ssh.SanitizerConfig{})
	var output strings.Builder
	output.WriteString(sanitizer.Push([]byte("prefix\x1bP")))
	payload := strings.Repeat("x", 4096)
	for range 512 {
		output.WriteString(sanitizer.Push([]byte(payload)))
		if retained := sanitizer.RetainedBytes(); retained > 3 {
			t.Fatalf("retained bytes = %d, want at most one UTF-8 tail", retained)
		}
	}
	output.WriteString(sanitizer.Push([]byte("\x1b\\suffix")))
	output.WriteString(sanitizer.Flush())
	if got := output.String(); got != "prefixsuffix" {
		t.Fatalf("control payload leaked or text changed: output length=%d", len(got))
	}
}

func TestSanitizeTerminalOutput_StripsUnsafeTerminalCommands(t *testing.T) {
	raw := []byte("plain\x1b[2J\x1b]2;title\a\x9b?1049htext")
	if got := ssh.SanitizeTerminalOutput(raw, nil); got != "plaintext" {
		t.Fatalf("unsafe terminal commands survived: %q", got)
	}
}
