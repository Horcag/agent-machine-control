package ssh_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

func sanitizeAtBoundary(input string, split int, cfg ssh.SanitizerConfig) string {
	sanitizer := ssh.NewStreamSanitizer(cfg)
	return sanitizer.Push([]byte(input[:split])) + sanitizer.Push([]byte(input[split:])) + sanitizer.Flush()
}

func TestStreamSanitizer_RedactsEveryChunkBoundary(t *testing.T) {
	exactSecret := "active-" + strings.Repeat("x", 24)
	configuredValue := "CFG-" + strings.Repeat("Q", 8)
	privateBody := strings.Repeat("M", 48)
	privateKeyMarker := "OPENSSH " + "PRIVATE KEY"
	cfg := ssh.SanitizerConfig{
		ExactSecrets: [][]byte{[]byte(exactSecret)},
		Patterns: []ssh.RedactionPattern{{
			Pattern:       regexp.MustCompile(`CFG-[A-Z]{8}`),
			MaxMatchBytes: len(configuredValue),
		}},
	}
	cases := []struct {
		name      string
		input     string
		forbidden []string
	}{
		{name: "exact active secret", input: "before " + exactSecret + " after", forbidden: []string{exactSecret}},
		{name: "configured regex", input: "before " + configuredValue + " after", forbidden: []string{configuredValue}},
		{name: "bearer", input: "Authorization: Bearer " + strings.Repeat("b", 24) + "\r\n", forbidden: []string{strings.Repeat("b", 24)}},
		{name: "password", input: "password=" + strings.Repeat("p", 24) + "\r\n", forbidden: []string{strings.Repeat("p", 24)}},
		{name: "password spaced", input: "password  =  " + strings.Repeat("s", 24) + "\r\n", forbidden: []string{strings.Repeat("s", 24)}},
		{name: "token", input: "token: " + strings.Repeat("t", 24) + "\r\n", forbidden: []string{strings.Repeat("t", 24)}},
		{name: "private key", input: "-----BEGIN " + privateKeyMarker + "-----\n" + privateBody + "\n-----END " + privateKeyMarker + "-----\n", forbidden: []string{privateBody}},
		{name: "osc", input: "safe\x1b]52;c;" + strings.Repeat("o", 24) + "\x07tail", forbidden: []string{strings.Repeat("o", 24), "52;c;"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for split := 0; split <= len(tc.input); split++ {
				out := sanitizeAtBoundary(tc.input, split, cfg)
				for _, forbidden := range tc.forbidden {
					if strings.Contains(out, forbidden) {
						t.Fatalf("sensitive form survived split boundary %d", split)
					}
				}
			}
		})
	}
}

func TestStreamSanitizer_StripsSplitCSIAndFlushesIncompleteSensitiveForms(t *testing.T) {
	input := "\x1b[31mred\x1b[0m"
	for split := 0; split <= len(input); split++ {
		if out := sanitizeAtBoundary(input, split, ssh.SanitizerConfig{}); out != "red" {
			t.Fatalf("CSI survived split boundary %d: %q", split, out)
		}
	}

	sanitizer := ssh.NewStreamSanitizer(ssh.SanitizerConfig{})
	privateKeyMarker := "OPENSSH " + "PRIVATE KEY"
	_ = sanitizer.Push([]byte("-----BEGIN " + privateKeyMarker + "-----\n" + strings.Repeat("Z", 16)))
	if out := sanitizer.Flush(); strings.Contains(out, strings.Repeat("Z", 16)) || !strings.Contains(out, "REDACTED") {
		t.Fatal("incomplete private key was not redacted on flush")
	}
}

func TestStreamSanitizer_AdversarialStreamsRetainBoundedState(t *testing.T) {
	tests := []struct {
		name        string
		prefix      string
		cfg         ssh.SanitizerConfig
		wantMarkers int
	}{
		{name: "unterminated bearer", prefix: "Bearer ", wantMarkers: 1},
		{name: "unterminated OSC", prefix: "\x1b]52;c;"},
		{name: "configured matcher", prefix: "CFG-", cfg: ssh.SanitizerConfig{Patterns: []ssh.RedactionPattern{{Pattern: regexp.MustCompile(`CFG-[A]+`), MaxMatchBytes: 4 << 20}}}, wantMarkers: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sanitizer := ssh.NewStreamSanitizer(tt.cfg)
			output := sanitizer.Push([]byte(tt.prefix))
			for range 512 {
				output += sanitizer.Push([]byte(strings.Repeat("A", 4096)))
				if retained := sanitizer.RetainedBytes(); retained > 64*1024 {
					t.Fatalf("retained bytes = %d, want <= 65536", retained)
				}
			}
			output += sanitizer.Flush()
			if strings.Contains(output, strings.Repeat("A", 4096)) {
				t.Fatal("attacker-controlled sensitive run was emitted")
			}
			if markers := strings.Count(output, "[REDACTED TRUNCATED]"); markers != tt.wantMarkers {
				t.Fatalf("overflow markers = %d, want %d", markers, tt.wantMarkers)
			}
		})
	}
}

func TestSanitizeTerminalOutput_OSC52(t *testing.T) {
	// Adversarial input: OSC 52 clipboard injection
	raw := []byte("\x1b]52;c;c2VjcmV0\x07Hello World")
	var pending []byte
	sanitized := ssh.SanitizeTerminalOutput(raw, &pending)

	if strings.Contains(sanitized, "52;c;") {
		t.Errorf("expected OSC 52 to be stripped, got: %q", sanitized)
	}
	if !strings.Contains(sanitized, "Hello World") {
		t.Errorf("expected clean text to remain, got: %q", sanitized)
	}
	if len(pending) != 0 {
		t.Errorf("expected no pending bytes, got %d", len(pending))
	}
}

func TestSanitizeTerminalOutput_UTF8Split(t *testing.T) {
	// Emoji 🔒 is 4 bytes: 0xF0, 0x9F, 0x94, 0x92
	chunk1 := []byte{0xF0, 0x9F}
	chunk2 := []byte{0x94, 0x92, 'A'}

	var pending []byte
	out1 := ssh.SanitizeTerminalOutput(chunk1, &pending)
	if out1 != "" {
		t.Errorf("expected empty output for incomplete UTF-8 chunk, got %q", out1)
	}
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending bytes, got %d", len(pending))
	}

	out2 := ssh.SanitizeTerminalOutput(chunk2, &pending)
	if out2 != "🔒A" {
		t.Errorf("expected '🔒A', got %q", out2)
	}
	if len(pending) != 0 {
		t.Errorf("expected pending buffer to be cleared, got %d", len(pending))
	}
}

func TestSanitizeTerminalOutput_InvalidUTF8(t *testing.T) {
	raw := []byte{0xFF, 0xFE, 'H', 'i'}
	var pending []byte
	out := ssh.SanitizeTerminalOutput(raw, &pending)

	if !strings.Contains(out, "Hi") {
		t.Errorf("expected 'Hi' in output, got %q", out)
	}
	if !strings.Contains(out, "\uFFFD") {
		t.Errorf("expected replacement character in output, got %q", out)
	}
}

func TestSanitizeTerminalOutput_ControlChars(t *testing.T) {
	// Mix of raw control chars (\x00, \x07, \x08, \x0c) with standard whitespace (\n, \t, \r)
	raw := []byte("Line 1\x00\x07\tTabbed\r\nLine 2\x08End")
	var pending []byte
	out := ssh.SanitizeTerminalOutput(raw, &pending)

	if strings.Contains(out, "\x00") || strings.Contains(out, "\x07") || strings.Contains(out, "\x08") {
		t.Errorf("unprintable control characters were not stripped: %q", out)
	}
	if !strings.Contains(out, "Line 1\tTabbed\r\nLine 2End") {
		t.Errorf("expected standard text to be preserved, got %q", out)
	}
}

func TestRedactSensitiveText(t *testing.T) {
	privateKeyBody := "MIIE" + "owIB" + strings.Repeat("A", 8) + "..."
	bearerToken := "secret" + "-" + "token" + "-" + "12345" + "." + "xyz=="
	passwordVal := "Super" + "Secret" + "Password" + "123!"
	tokenVal := "abc" + "def" + "123" + "456" + "7890"

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "private key block",
			input:    "Error: -----BEGIN RSA PRIVATE KEY-----\n" + privateKeyBody + "\n-----END RSA PRIVATE KEY-----\nFailed",
			expected: "Error: [REDACTED PRIVATE KEY]\nFailed",
		},
		{
			name:     "bearer token",
			input:    "Auth: Bearer " + bearerToken + "\nSuccess",
			expected: "Auth: Bearer [REDACTED]\nSuccess",
		},
		{
			name:     "password field",
			input:    "Connecting with password=" + passwordVal + "\nConnected",
			expected: "Connecting with password=[REDACTED]\nConnected",
		},
		{
			name:     "token field",
			input:    "Using token: " + tokenVal + "\nDone",
			expected: "Using token: [REDACTED]\nDone",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := ssh.RedactSensitiveText(tt.input)
			if actual != tt.expected {
				t.Errorf("RedactSensitiveText() = %q, want %q", actual, tt.expected)
			}
		})
	}
}

func TestStripANSI(t *testing.T) {
	input := "\x1b[32mGreen Text\x1b[0m and \x1b[1;31mBold Red\x1b[0m"
	out := ssh.StripANSI(input)
	if out != "Green Text and Bold Red" {
		t.Errorf("expected plain text, got %q", out)
	}

	if empty := ssh.StripANSI(""); empty != "" {
		t.Errorf("expected empty string from StripANSI, got %q", empty)
	}
	if empty := ssh.RedactSensitiveText(""); empty != "" {
		t.Errorf("expected empty string from RedactSensitiveText, got %q", empty)
	}
	if empty := ssh.SanitizeTerminalOutput(nil, nil); empty != "" {
		t.Errorf("expected empty string from SanitizeTerminalOutput, got %q", empty)
	}
}

func TestSanitizeTerminalOutput_DEL(t *testing.T) {
	raw := []byte("hello\x7fworld")
	out := ssh.SanitizeTerminalOutput(raw, nil)
	if out != "helloworld" {
		t.Errorf("expected DEL to be stripped, got %q", out)
	}

	// OSC with ESC \ terminator
	oscEsc := []byte("\x1b]52;c;data\x1b\\Clean")
	outOsc := ssh.SanitizeTerminalOutput(oscEsc, nil)
	if !strings.Contains(outOsc, "Clean") || strings.Contains(outOsc, "data") {
		t.Errorf("expected clean string without OSC data, got %q", outOsc)
	}

	// pendingBuf with data and nil raw
	pending := []byte("from-pending")
	outPending := ssh.SanitizeTerminalOutput(nil, &pending)
	if outPending != "from-pending" {
		t.Errorf("expected 'from-pending', got %q", outPending)
	}
}
