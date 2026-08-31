package lease

import (
	"os"
	"testing"
)

func TestDetectRuntimeIDAndNativeStartTime(t *testing.T) {
	if rid := detectRuntimeID(); rid == "" {
		t.Fatal("expected non-empty runtime ID")
	}
	start := platformProcessStartTime(os.Getpid())
	if start == "" {
		t.Fatal("current process has no native start identity")
	}
	alive, err := platformProcessAlive(os.Getpid(), start)
	if err != nil || !alive {
		t.Fatalf("current native process identity alive=%v err=%v", alive, err)
	}
	alive, err = platformProcessAlive(os.Getpid(), "impossible-start-identity")
	if err != nil || alive {
		t.Fatalf("recycled identity alive=%v err=%v", alive, err)
	}
}
