package domain

import (
	"strings"
	"testing"
)

func guestFixtureParams(operation string) map[string]any {
	return map[string]any{"operation": operation, "configuration_sha256": strings.Repeat("a", 64), "payload_sha256": strings.Repeat("b", 64), "size": int64(0), "max_output": int64(0), "artifact_ids": []string{}}
}
func TestGuestOperationSchemaFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"foreign path", func(p map[string]any) { p["path"] = `C:\foreign` }},
		{"fractional size", func(p map[string]any) { p["size"] = 0.5 }},
		{"invalid digest", func(p map[string]any) { p["payload_sha256"] = "invalid" }},
		{"other operation", func(p map[string]any) { p["operation"] = "prepare" }},
		{"oversized input", func(p map[string]any) { p["size"] = int64(1<<40) + 1 }},
		{"foreign references", func(p map[string]any) { p["artifact_ids"] = []string{`C:\foreign`} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := guestFixtureParams("put")
			tc.mutate(p)
			if ValidateOperationParameters("guest.put", p) == nil {
				t.Fatal("accepted invalid guest parameters")
			}
		})
	}
}
func TestGuestManifestIsBoundedAndUnique(t *testing.T) {
	p := guestFixtureParams("remove-owned")
	p["artifact_ids"] = []string{strings.Repeat("a", 32)}
	if err := ValidateOperationParameters("guest.remove-owned", p); err != nil {
		t.Fatal(err)
	}
	p["artifact_ids"] = []string{strings.Repeat("a", 32), strings.Repeat("a", 32)}
	if ValidateOperationParameters("guest.remove-owned", p) == nil {
		t.Fatal("duplicate references accepted")
	}
	ids := make([]string, 129)
	p["artifact_ids"] = ids
	if ValidateOperationParameters("guest.remove-owned", p) == nil {
		t.Fatal("unbounded manifest accepted")
	}
}
func TestGuestCommandOutputBoundsAndScopes(t *testing.T) {
	p := guestFixtureParams("exec")
	p["size"] = int64(10)
	p["max_output"] = int64(10)
	if err := ValidateOperationParameters("guest.exec", p); err != nil {
		t.Fatal(err)
	}
	p["max_output"] = int64(16*1024*1024 + 1)
	if ValidateOperationParameters("guest.exec", p) == nil {
		t.Fatal("unbounded command output")
	}
	scopes := NewScopeSet(GuestOperationScopes("guest.exec")...)
	if !scopes.Has(ScopeSessionWrite) || !scopes.Has(ScopeEvidenceCapture) {
		t.Fatal("command scopes lack effect or sensitive-output authority")
	}
}
