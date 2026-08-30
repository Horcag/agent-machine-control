package auth_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestAuth_LoadOrCreate(t *testing.T) {
	dir := t.TempDir()

	_, err := auth.LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate failed: %v", err)
	}

	opToken, err := auth.ReadTokenFile(dir, auth.TokenTypeOperator)
	if err != nil {
		t.Fatalf("ReadTokenFile operator failed: %v", err)
	}
	if len(opToken) < 64 {
		t.Errorf("expected operator token length >= 64, got %d", len(opToken))
	}

	agToken, err := auth.ReadTokenFile(dir, auth.TokenTypeAgentMCP)
	if err != nil {
		t.Fatalf("ReadTokenFile agent-mcp failed: %v", err)
	}
	if len(agToken) < 64 {
		t.Errorf("expected agent-mcp token length >= 64, got %d", len(agToken))
	}

	if opToken == agToken {
		t.Errorf("operator and agent-mcp tokens must be distinct")
	}

	// Check file permissions (mode 0600)
	opFi, err := os.Stat(filepath.Join(dir, auth.OperatorTokenFileName))
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if runtime.GOOS != "windows" && opFi.Mode().Perm() != 0600 {
		t.Errorf("expected token mode 0600, got %o", opFi.Mode().Perm())
	}
}

func TestAuth_Authenticate(t *testing.T) {
	dir := t.TempDir()
	store, err := auth.LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate failed: %v", err)
	}

	opToken, _ := auth.ReadTokenFile(dir, auth.TokenTypeOperator)
	agToken, _ := auth.ReadTokenFile(dir, auth.TokenTypeAgentMCP)

	// Authenticate operator
	act, scopes, ok := store.Authenticate(opToken)
	if !ok || act == nil {
		t.Fatalf("expected operator auth to succeed")
	}
	wantOperatorScopes := []string{
		domain.ScopeAuditRead,
		domain.ScopeEvidenceCapture,
		domain.ScopeMachineRead,
		domain.ScopeMachineWrite,
		domain.ScopeOperationCancel,
		domain.ScopeSessionAdmin,
		domain.ScopeSessionClose,
		domain.ScopeSessionOpen,
		domain.ScopeSessionRead,
		domain.ScopeSessionWrite,
	}
	if !slices.Equal(scopes, wantOperatorScopes) {
		t.Errorf("operator scopes = %v, want %v", scopes, wantOperatorScopes)
	}

	// Authenticate agent
	act, scopes, ok = store.Authenticate(agToken)
	if !ok || act == nil {
		t.Fatalf("expected agent auth to succeed")
	}
	if act.EffectiveActor != "agent:mcp-local" {
		t.Errorf("expected agent:mcp-local, got %s", act.EffectiveActor)
	}
	wantAgentScopes := []string{
		domain.ScopeEvidenceCapture,
		domain.ScopeMachineRead,
		domain.ScopeMachineWrite,
		domain.ScopeSessionClose,
		domain.ScopeSessionOpen,
		domain.ScopeSessionRead,
		domain.ScopeSessionWrite,
	}
	if !slices.Equal(scopes, wantAgentScopes) {
		t.Errorf("agent scopes = %v, want %v", scopes, wantAgentScopes)
	}

	// Authenticate invalid
	if _, _, ok = store.Authenticate("invalid-token"); ok {
		t.Errorf("expected invalid token to fail")
	}

	if _, _, ok = store.Authenticate(""); ok {
		t.Errorf("expected empty token to fail")
	}
}

func TestAuth_ActiveBearerSecretsReturnsIndependentCopies(t *testing.T) {
	store, err := auth.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first := store.ActiveBearerSecrets()
	if len(first) != 2 || len(first[0]) != 64 || len(first[1]) != 64 {
		t.Fatalf("unexpected active bearer secret shape")
	}
	first[0][0] ^= 0xff
	first[1][0] ^= 0xff
	second := store.ActiveBearerSecrets()
	if first[0][0] == second[0][0] || first[1][0] == second[1][0] {
		t.Fatal("caller mutation changed auth-store token memory")
	}
	if string(second[0]) == string(second[1]) {
		t.Fatal("active bearer tokens must remain distinct")
	}
}

func TestAuth_LoadExistingTokens(t *testing.T) {
	dir := t.TempDir()
	customOpToken := strings.Repeat("a", 64)
	customAgToken := strings.Repeat("b", 64)

	if err := os.WriteFile(filepath.Join(dir, auth.OperatorTokenFileName), []byte(customOpToken+"\n"), 0600); err != nil {
		t.Fatalf("write op token failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, auth.AgentMCPTokenFileName), []byte(customAgToken+"\r\n"), 0600); err != nil {
		t.Fatalf("write ag token failed: %v", err)
	}

	store, err := auth.LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate failed: %v", err)
	}

	_, _, ok := store.Authenticate(customOpToken)
	if !ok {
		t.Errorf("failed to authenticate existing op token")
	}

	_, _, ok = store.Authenticate(customAgToken)
	if !ok {
		t.Errorf("failed to authenticate existing ag token")
	}
}

func TestAuth_Errors(t *testing.T) {
	dir := t.TempDir()

	// Unknown token type
	_, err := auth.ReadTokenFile(dir, "unknown-type")
	if err == nil {
		t.Errorf("expected error for unknown token type")
	}

	// Missing file
	_, err = auth.ReadTokenFile(dir, auth.TokenTypeOperator)
	if err == nil {
		t.Errorf("expected error for missing token file")
	}

	// Corrupt short token
	_ = os.WriteFile(filepath.Join(dir, auth.OperatorTokenFileName), []byte("short\n"), 0600)
	_ = os.WriteFile(filepath.Join(dir, auth.AgentMCPTokenFileName), []byte("short\n"), 0600)
	_, err = auth.LoadOrCreate(dir)
	if err == nil {
		t.Errorf("expected error for short token file")
	}

	// Non-hex token
	_ = os.WriteFile(filepath.Join(dir, auth.OperatorTokenFileName), []byte(strings.Repeat("z", 64)+"\n"), 0600)
	_, err = auth.LoadOrCreate(dir)
	if err == nil {
		t.Errorf("expected error for non-hex token file")
	}

	if runtime.GOOS != "windows" {
		// Insecure POSIX permissions.
		insecureFile := filepath.Join(dir, auth.OperatorTokenFileName)
		_ = os.WriteFile(insecureFile, []byte(strings.Repeat("a", 64)+"\n"), 0600)
		_ = os.Chmod(insecureFile, 0644)
		_, err = auth.LoadOrCreate(dir)
		if err == nil {
			t.Errorf("expected error for insecure permissions token file")
		}
	}

	// Oversize file
	_ = os.WriteFile(filepath.Join(dir, auth.OperatorTokenFileName), []byte(strings.Repeat("a", 100)+"\n"), 0600)
	_, err = auth.LoadOrCreate(dir)
	if err == nil {
		t.Errorf("expected error for oversize token file")
	}
}

func TestAuth_SymlinkRejection(t *testing.T) {
	dir := t.TempDir()
	realFile := filepath.Join(dir, "real.token")
	_ = os.WriteFile(realFile, []byte(strings.Repeat("a", 64)+"\n"), 0600)

	symlinkFile := filepath.Join(dir, auth.OperatorTokenFileName)
	if err := os.Symlink(realFile, symlinkFile); err != nil {
		t.Skip("symlinks not supported in environment")
	}

	_, err := auth.LoadOrCreate(dir)
	if err == nil {
		t.Errorf("expected LoadOrCreate to fail on symlinked token file")
	}

	_, err = auth.ReadTokenFile(dir, auth.TokenTypeOperator)
	if err == nil {
		t.Errorf("expected ReadTokenFile to fail on symlinked token file")
	}
}

func TestAuth_StoreCoverage(t *testing.T) {
	dir := t.TempDir()
	store, err := auth.LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate failed: %v", err)
	}

	// Empty bearer token
	act, scopes, ok := store.Authenticate("   ")
	if ok || act != nil || scopes != nil {
		t.Errorf("expected empty token to fail")
	}

	// Empty directory path
	_, err = auth.LoadOrCreate("")
	if err == nil {
		t.Errorf("expected error for empty authDir")
	}
}

func TestAuth_ReadEmptyTokenFile(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, auth.OperatorTokenFileName), []byte("  \n"), 0600)
	_, err := auth.ReadTokenFile(dir, auth.TokenTypeOperator)
	if err == nil {
		t.Errorf("expected error reading empty token file")
	}
}

func TestAuth_TokensExtraCoverage(t *testing.T) {
	dir := t.TempDir()
	store, err := auth.LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("LoadOrCreate failed: %v", err)
	}

	// Read agent token
	agToken, err := auth.ReadTokenFile(dir, auth.TokenTypeAgentMCP)
	if err != nil || len(agToken) == 0 {
		t.Fatalf("ReadTokenFile agent-mcp failed: %v", err)
	}

	// Constant time compare mismatch length
	act, _, ok := store.Authenticate("short")
	if ok || act != nil {
		t.Errorf("expected failure for short token")
	}

	// Unknown token type
	if _, err := auth.ReadTokenFile(dir, auth.TokenType("invalid")); err == nil {
		t.Errorf("expected error for invalid token type")
	}

	// Token file is a directory
	dirToken := filepath.Join(t.TempDir(), "operator.token")
	_ = os.Mkdir(dirToken, 0700)
	if _, err := auth.ReadTokenFile(filepath.Dir(dirToken), auth.TokenTypeOperator); err == nil {
		t.Errorf("expected error when token file is a directory")
	}
}

func TestAuth_InjectablePrincipalResolver(t *testing.T) {
	dir := t.TempDir()

	// 1. Successful custom resolver
	customPrincipal := "operator:custom-test-user"
	store, err := auth.LoadOrCreate(dir, auth.WithPrincipalResolver(func() (string, error) {
		return customPrincipal, nil
	}))
	if err != nil {
		t.Fatalf("LoadOrCreate with custom resolver failed: %v", err)
	}

	opToken, err := auth.ReadTokenFile(dir, auth.TokenTypeOperator)
	if err != nil {
		t.Fatalf("ReadTokenFile failed: %v", err)
	}
	act, _, ok := store.Authenticate(opToken)
	if !ok || act == nil || string(act.EffectiveActor) != customPrincipal {
		t.Fatalf("expected effective actor %s, got %+v", customPrincipal, act)
	}

	// 2. Failing custom resolver -> LoadOrCreate must fail closed
	dir2 := t.TempDir()
	_, err = auth.LoadOrCreate(dir2, auth.WithPrincipalResolver(func() (string, error) {
		return "", errors.New("simulated identity resolution failure")
	}))
	if err == nil {
		t.Errorf("expected LoadOrCreate to fail when principal resolver returns error")
	}

	// 3. Custom resolver returning invalid actor ID (empty or invalid whitespace) -> LoadOrCreate must fail closed
	dir3 := t.TempDir()
	_, err = auth.LoadOrCreate(dir3, auth.WithPrincipalResolver(func() (string, error) {
		return "  invalid-with-leading-spaces", nil
	}))
	if err == nil {
		t.Errorf("expected LoadOrCreate to fail when principal resolver returns invalid actor ID")
	}
}
