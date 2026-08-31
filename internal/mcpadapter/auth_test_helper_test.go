package mcpadapter

import (
	"testing"

	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

func createTestAgentToken(t *testing.T, stateDir string) string {
	t.Helper()
	sd, err := statedir.Resolve(stateDir)
	if err != nil {
		t.Fatalf("resolve test state directory: %v", err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatalf("ensure test state directory: %v", err)
	}
	if _, err := auth.LoadOrCreate(sd.AuthDir(), auth.WithPrincipalResolver(func() (string, error) {
		return "operator:mcp-test", nil
	})); err != nil {
		t.Fatalf("create test auth tokens: %v", err)
	}
	token, err := auth.ReadTokenFile(sd.AuthDir(), auth.TokenTypeAgentMCP)
	if err != nil {
		t.Fatalf("read test agent token: %v", err)
	}
	return token
}
