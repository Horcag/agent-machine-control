package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestClipboardGuardPresenceValidationAndFingerprint(t *testing.T) {
	base := DesktopRequest{RequestID: strings.Repeat("a", 32), Deadline: "2026-10-03T20:00:00Z", Action: "clipboard.set"}
	zero := uint32(0)
	empty := []uint32{}
	guarded := base
	guarded.ExpectedSequence = &zero
	guarded.ExpectedInventory = &empty
	if guarded.Validate() != nil {
		t.Fatal("explicit zero and empty inventory rejected")
	}
	a, _ := json.Marshal(base)
	b, _ := json.Marshal(guarded)
	if strings.Contains(string(a), "expected_sequence") || !strings.Contains(string(b), `"expected_sequence":0`) || !strings.Contains(string(b), `"expected_inventory":[]`) {
		t.Fatal("guard presence lost")
	}
	if DesktopActionParameters(base)["payload_sha256"] == DesktopActionParameters(guarded)["payload_sha256"] {
		t.Fatal("approval fingerprint ignores guard")
	}
	for _, change := range []func(*DesktopRequest){
		func(r *DesktopRequest) { r.ExpectedSequence = nil }, func(r *DesktopRequest) { r.ExpectedInventory = nil },
		func(r *DesktopRequest) { v := []uint32(nil); r.ExpectedInventory = &v },
		func(r *DesktopRequest) { v := []uint32{13, 13}; r.ExpectedInventory = &v },
		func(r *DesktopRequest) { v := []uint32{0}; r.ExpectedInventory = &v },
		func(r *DesktopRequest) { v := []uint32{14, 13}; r.ExpectedInventory = &v },
		func(r *DesktopRequest) { r.Action = "clipboard.get" },
	} {
		r := guarded
		change(&r)
		if r.Validate() == nil {
			t.Fatal("invalid guard accepted")
		}
	}
}
