//go:build linux

package lease

import "testing"

func TestParseLinuxStartTime(t *testing.T) {
	statContent := "1234 (bash) S 1 1234 1234 0 -1 4194304 100 0 0 0 10 20 0 0 20 0 1 0 54321 12345 100"
	if got := parseLinuxStartTime(statContent); got != "54321" {
		t.Fatalf("start time = %q, want 54321", got)
	}
	if got := parseLinuxStartTime("no closing paren"); got != "" {
		t.Fatalf("malformed start time = %q", got)
	}
}
