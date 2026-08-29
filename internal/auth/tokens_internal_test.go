package auth

import "testing"

func TestSanitizePrincipal(t *testing.T) {
	result := sanitizePrincipal("DOMAIN\\user name")
	if result != "domain-user-name" {
		t.Errorf("expected domain-user-name, got %s", result)
	}
}
