package lease

import (
	"errors"
	"os"
	"testing"
)

func TestParseLinuxStartTime(t *testing.T) {
	// Standard Linux /proc/[pid]/stat line snippet
	statContent := "1234 (bash) S 1 1234 1234 0 -1 4194304 100 0 0 0 10 20 0 0 20 0 1 0 54321 12345 100"
	startTime := parseLinuxStartTime(statContent)
	if startTime != "54321" {
		t.Errorf("expected 54321, got %q", startTime)
	}

	// Malformed
	if s := parseLinuxStartTime(""); s != "" {
		t.Errorf("expected empty for empty stat, got %q", s)
	}
	if s := parseLinuxStartTime("no closing paren"); s != "" {
		t.Errorf("expected empty for invalid stat, got %q", s)
	}
}

func TestCheckPosixProcessAlive(t *testing.T) {
	// Current process is alive
	alive, err := checkPosixProcessAlive(os.Getpid())
	if err != nil || !alive {
		t.Errorf("expected current process alive, got %v, err=%v", alive, err)
	}

	// Dead process
	dead, err := checkPosixProcessAlive(999999999)
	if err != nil || dead {
		t.Errorf("expected large PID dead, got %v, err=%v", dead, err)
	}
}

func TestErrorMatchers(t *testing.T) {
	if !errorsIsNoSuchProcess(errors.New("os: no such process")) {
		t.Errorf("expected true for no such process")
	}
	if !errorsIsNoSuchProcess(errors.New("process already finished")) {
		t.Errorf("expected true for process already finished")
	}
	if errorsIsNoSuchProcess(errors.New("other error")) {
		t.Errorf("expected false for other error")
	}

	if !errorsIsPermission(errors.New("operation not permitted")) {
		t.Errorf("expected true for operation not permitted")
	}
	if !errorsIsPermission(errors.New("permission denied")) {
		t.Errorf("expected true for permission denied")
	}
	if errorsIsPermission(errors.New("other error")) {
		t.Errorf("expected false for other error")
	}
}

func TestDetectRuntimeID_And_ReadStartTime(t *testing.T) {
	rid := detectRuntimeID()
	if rid == "" {
		t.Errorf("expected non-empty runtime ID")
	}
	st := readProcessStartTime(os.Getpid())
	if st == "" {
		t.Logf("empty start time on non-linux or inaccessible /proc")
	}
	stNonExistent := readProcessStartTime(999999999)
	if stNonExistent != "" {
		t.Errorf("expected empty start time for non-existent PID, got %q", stNonExistent)
	}
}
