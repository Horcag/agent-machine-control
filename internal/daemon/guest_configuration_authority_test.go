package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestGuestConfigurationPreservesRequestedAuthority(t *testing.T) {
	provider, _, _, _ := newGuestConfigurationFixture(t)
	server := provider.server
	token, err := auth.ReadTokenFile(server.stateDir.AuthDir(), auth.TokenTypeAgentMCP)
	if err != nil {
		t.Fatal(err)
	}
	full, _, ok := server.authStore.Authenticate(token)
	if !ok {
		t.Fatal("fixture authentication failed")
	}
	ref := domain.MachineRef("local:11111111-1111-4111-8111-111111111111")
	baseline, err := provider.Current(t.Context(), ref, *full)
	if err != nil {
		t.Fatal(err)
	}
	narrow := full.Clone()
	delete(narrow.CallerPermissions, domain.ScopeMachineRead)
	delete(narrow.EffectivePermissions, domain.ScopeMachineRead)
	delete(narrow.CallerPermissions, domain.ScopeMachineWrite)
	delete(narrow.EffectivePermissions, domain.ScopeMachineWrite)
	config, err := provider.Current(t.Context(), ref, narrow)
	if err != nil || !reflect.DeepEqual(config.Caller, narrow) || config.Generation != baseline.Generation {
		t.Fatalf("requested authority expanded or generation changed: caller=%+v err=%v", config.Caller, err)
	}
	config.Caller.CallerPermissions[domain.ScopeOperationAdmin] = struct{}{}
	config.Caller.EffectivePermissions[domain.ScopeOperationAdmin] = struct{}{}
	if narrow.HasScope(domain.ScopeOperationAdmin) || full.HasScope(domain.ScopeOperationAdmin) {
		t.Fatal("configuration aliases caller or authenticated authority")
	}
	if _, err := provider.Current(t.Context(), ref, config.Caller); !errors.Is(err, app.ErrGuestAuthority) {
		t.Fatalf("requested privilege expansion accepted: %v", err)
	}
	if err := os.WriteFile(filepath.Join(server.stateDir.AuthDir(), auth.AgentMCPTokenFileName), []byte(strings.Repeat("a", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Current(t.Context(), ref, narrow); !errors.Is(err, app.ErrGuestAuthority) {
		t.Fatalf("rotated agent credential accepted: %v", err)
	}
}
