package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/Horcag/agent-machine-control/internal/app"
	"testing"
)

func TestGuestDirectRecoveryIndependent(t *testing.T) {
	var out, err bytes.Buffer
	code := runGuest(context.Background(), nil, true, "missing", []string{"prepare"}, &out, &err)
	if code != ExitBackendUnavailable || out.Len() != 0 {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), err.String())
	}
}
func TestGuestRejectsUnknownOperationBeforeDiscovery(t *testing.T) {
	var out, err bytes.Buffer
	code := runGuest(context.Background(), nil, false, "missing", []string{"adopt-arbitrary-root"}, &out, &err)
	if code != ExitUsage {
		t.Fatalf("code=%d err=%s", code, err.String())
	}
}

func TestGuestExecExitCodePreservesVerifiedResult(t *testing.T) {
	for _, code := range []int{0, 37, -1, 256} {
		var out bytes.Buffer
		result := app.GuestOperationResult{ExitCode: code}
		got := finishGuestCLI(&out, "exec", result)
		want := code
		if code < 0 || code > 255 {
			want = 1
		}
		if got != want {
			t.Fatalf("native code=%d CLI code=%d want=%d", code, got, want)
		}
		var serialized app.GuestOperationResult
		if json.Unmarshal(out.Bytes(), &serialized) != nil || serialized.ExitCode != code {
			t.Fatal("verified execution result was relabeled")
		}
	}
}
