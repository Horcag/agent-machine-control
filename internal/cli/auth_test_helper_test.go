package cli_test

import (
	"testing"

	"github.com/Horcag/agent-machine-control/internal/auth"
)

func createTestOperatorToken(t *testing.T, authDir string) string {
	t.Helper()
	if _, err := auth.LoadOrCreate(authDir, auth.WithPrincipalResolver(func() (string, error) {
		return "operator:cli-test", nil
	})); err != nil {
		t.Fatalf("create test auth tokens: %v", err)
	}
	token, err := auth.ReadTokenFile(authDir, auth.TokenTypeOperator)
	if err != nil {
		t.Fatalf("read test operator token: %v", err)
	}
	return token
}
