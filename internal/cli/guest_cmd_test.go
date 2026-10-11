package cli

import (
	"bytes"
	"context"
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
