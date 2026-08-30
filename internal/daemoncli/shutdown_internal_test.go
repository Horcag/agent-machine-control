package daemoncli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestShutdownFailureExitsNonZeroWithSanitizedMessage(t *testing.T) {
	var stderr bytes.Buffer
	code := reportShutdownFailure(&stderr, errors.New("sensitive synthetic detail"))
	if code == ExitSuccess {
		t.Fatal("shutdown failure returned success")
	}
	if got := stderr.String(); !strings.Contains(got, "shutdown failed") || strings.Contains(got, "sensitive synthetic detail") {
		t.Fatalf("shutdown message = %q, want sanitized operator-facing failure", got)
	}
}
