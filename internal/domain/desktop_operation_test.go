package domain

import (
	"strings"
	"testing"
)

func TestDesktopOperationRejectsPlaintextAndUnknownActions(t *testing.T) {
	p := DesktopActionParameters(DesktopRequest{Action: "clipboard.set", Text: "secret"})
	if err := ValidateOperationParameters("desktop.action", p); err != nil {
		t.Fatal(err)
	}
	p["text"] = "secret"
	if err := ValidateOperationParameters("desktop.action", p); err == nil {
		t.Fatal("plaintext accepted")
	}
	delete(p, "text")
	p["action"] = "host.launch"
	if err := ValidateOperationParameters("desktop.action", p); err == nil {
		t.Fatal("unknown action accepted")
	}
}

func TestConsoleLabParameterSchemas(t *testing.T) {
	p := map[string]any{"grant_id": strings.Repeat("a", 32), "beneficiary": "agent:mcp-local", "enrollment_identity": strings.Repeat("b", 64), "expires_at": "2026-10-02T12:00:00Z"}
	if err := ValidateOperationParameters("console.lab.grant.issue", p); err != nil {
		t.Fatal(err)
	}
	p["enrollment_identity"] = strings.Repeat("B", 64)
	if err := ValidateOperationParameters("console.lab.grant.issue", p); err == nil {
		t.Fatal("noncanonical identity accepted")
	}
	r := map[string]any{"grant_id": strings.Repeat("c", 32)}
	if err := ValidateOperationParameters("console.lab.grant.revoke", r); err != nil {
		t.Fatal(err)
	}
	r["target"] = "default"
	if err := ValidateOperationParameters("console.lab.grant.revoke", r); err == nil {
		t.Fatal("additional parameter accepted")
	}
}
