package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

func TestGuestWorkspaceRequiresOriginalProtectedRecord(t *testing.T) {
	sd, err := statedir.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	provider := guestConfigurationProvider{server: &Server{stateDir: sd}}
	target := domain.MachineRef("local:11111111-1111-4111-8111-111111111111")
	if _, err = provider.workspaceRecord(t.Context(), target); !errors.Is(err, app.ErrGuestWorkspaceUnprovisioned) {
		t.Fatalf("missing record error=%v", err)
	}
	directory := filepath.Join(sd.Root(), "guest-workspaces")
	if err = os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(target))
	path := filepath.Join(directory, hex.EncodeToString(sum[:])+".json")
	record := GuestWorkspaceRecord{Target: string(target), ProvisioningID: "synthetic-original-provision"}
	data, _ := json.Marshal(record)
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := provider.workspaceRecord(t.Context(), target)
	if err != nil || loaded.ProvisioningID != record.ProvisioningID {
		t.Fatalf("record=%+v err=%v", loaded, err)
	}
	record.Target = "local:22222222-2222-4222-8222-222222222222"
	data, _ = json.Marshal(record)
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.workspaceRecord(t.Context(), target); err == nil {
		t.Fatal("foreign target record accepted")
	}
}
func TestGuestCurrentActorRejectsRevokedTokenAndDelegation(t *testing.T) {
	sd, err := statedir.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(sd.AuthDir(), 0700); err != nil {
		t.Fatal(err)
	}
	store, err := auth.LoadOrCreate(sd.AuthDir(), auth.WithPrincipalResolver(func() (string, error) { return "operator:synthetic", nil }))
	if err != nil {
		t.Fatal(err)
	}
	token, err := auth.ReadTokenFile(sd.AuthDir(), auth.TokenTypeOperator)
	if err != nil {
		t.Fatal(err)
	}
	caller, _, ok := store.Authenticate(token)
	if !ok {
		t.Fatal("fixture authentication failed")
	}
	provider := guestConfigurationProvider{server: &Server{stateDir: sd, authStore: store}}
	if _, err = provider.currentActor(context.Background(), *caller); err != nil {
		t.Fatal(err)
	}
	delegated := caller.Clone()
	delegated.EffectiveActor = "agent:mcp-local"
	if _, err = provider.currentActor(t.Context(), delegated); err == nil {
		t.Fatal("delegation adopted")
	}
	if err = os.WriteFile(filepath.Join(sd.AuthDir(), auth.OperatorTokenFileName), []byte(strings.Repeat("a", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.currentActor(t.Context(), *caller); !errors.Is(err, app.ErrGuestAuthority) {
		t.Fatalf("cached token authority accepted after rotation: %v", err)
	}
}
