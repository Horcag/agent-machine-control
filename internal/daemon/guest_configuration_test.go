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
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/Horcag/agent-machine-control/internal/target"
)

func TestGuestWorkspaceRequiresOriginalProtectedRecord(t *testing.T) {
	sd, err := statedir.Resolve(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	provider := guestConfigurationProvider{server: &Server{stateDir: sd}}
	targetRef := domain.MachineRef("local:11111111-1111-4111-8111-111111111111")
	if _, err = provider.workspaceRecord(t.Context(), targetRef); !errors.Is(err, app.ErrGuestWorkspaceUnprovisioned) {
		t.Fatalf("missing record error=%v", err)
	}
	directory := filepath.Join(sd.Root(), "guest-workspaces")
	if err = statedir.EnsurePrivateDirectory(directory); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(targetRef))
	path := filepath.Join(directory, hex.EncodeToString(sum[:])+".json")
	record := GuestWorkspaceRecord{Target: string(targetRef), ProvisioningID: "synthetic-original-provision"}
	data, _ := json.Marshal(record)
	if err = writeGuestPrivateFixture(t, path, data); err != nil {
		t.Fatal(err)
	}
	loaded, err := provider.workspaceRecord(t.Context(), targetRef)
	if err != nil || loaded.ProvisioningID != record.ProvisioningID {
		t.Fatalf("record=%+v err=%v", loaded, err)
	}
	record.Target = "local:22222222-2222-4222-8222-222222222222"
	data, _ = json.Marshal(record)
	if err = writeGuestPrivateFixture(t, path, data); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.workspaceRecord(t.Context(), targetRef); err == nil {
		t.Fatal("foreign target record accepted")
	}
}
func TestGuestCurrentActorRejectsRevokedTokenAndDelegation(t *testing.T) {
	sd, err := statedir.Resolve(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err = statedir.EnsurePrivateDirectory(sd.AuthDir()); err != nil {
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

// The configuration and dial contract are distinct: provider defaults and
// endpoint selection must remain in the same revocable generation.
func TestGuestConfigurationBindsEffectiveRouteAndFrozenDefaults(t *testing.T) {
	provider, keys, caller, config := newGuestConfigurationFixture(t)
	ref := domain.MachineRef("local:11111111-1111-4111-8111-111111111111")
	original, err := provider.Current(t.Context(), ref, *caller)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := freezeGuestKeys(t.Context(), keys, ref)
	if err != nil {
		t.Fatal(err)
	}
	user, _ := frozen.GetGuestUser(t.Context(), ref)
	raw, _ := frozen.GetMachineConfig(ref)
	if user != "Administrator" || raw.User != "" {
		t.Fatalf("effective default lost or raw config rewritten: %q %+v", user, raw)
	}
	snapshot := provider
	snapshot.keys = frozen
	bound, err := snapshot.Current(t.Context(), ref, *caller)
	if err != nil || bound.Generation != original.Generation {
		t.Fatalf("frozen route not generation-bound: %+v %v", bound, err)
	}
	assertGuestEffectiveRouteDrift(t, provider, keys, caller, ref, original.Generation, config.Endpoint, frozen)
}

func assertGuestEffectiveRouteDrift(t *testing.T, provider guestConfigurationProvider, keys *effectiveGuestRoute, caller *domain.ActorContext, ref domain.MachineRef, originalGeneration, originalEndpoint string, frozen *frozenGuestKeys) {
	t.Helper()
	keys.endpoint = "127.0.0.1:65001"
	changed, err := provider.Current(t.Context(), ref, *caller)
	if err != nil || changed.Generation == originalGeneration {
		t.Fatalf("effective endpoint drift not fenced: %v", err)
	}
	frozenEndpoint, _ := frozen.GetGuestEndpoint(t.Context(), ref)
	if frozenEndpoint != originalEndpoint {
		t.Fatal("frozen endpoint drifted")
	}
	frozenCustom, err := freezeGuestKeys(t.Context(), keys, ref)
	if err != nil {
		t.Fatal(err)
	}
	custom, _ := frozenCustom.GetGuestEndpoint(t.Context(), ref)
	if custom != keys.endpoint {
		t.Fatal("effective custom endpoint lost")
	}
	keys.user = "changed-user"
	changedUser, err := provider.Current(t.Context(), ref, *caller)
	if err != nil || changedUser.Generation == changed.Generation {
		t.Fatal("effective user drift not fenced")
	}
	if _, err = provider.retirementCommandRunner(t.Context(), ref, *caller, originalGeneration); !errors.Is(err, app.ErrGuestAuthority) {
		t.Fatalf("retirement accepted stale dial generation: %v", err)
	}
}

func newGuestConfigurationFixture(t *testing.T) (guestConfigurationProvider, *effectiveGuestRoute, *domain.ActorContext, guestssh.MachineSSHConfig) {
	t.Helper()
	sd, targets, local, _ := guestEnrolledKeyFixture(t)
	path := filepath.Join(sd.MachinesDir(), "11111111-1111-4111-8111-111111111111", "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config guestssh.MachineSSHConfig
	if err = json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	config.User = ""
	data, _ = json.Marshal(config)
	if err = writeGuestPrivateFixture(t, path, data); err != nil {
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
		t.Fatal("fixture auth")
	}
	ref := domain.MachineRef("local:11111111-1111-4111-8111-111111111111")
	directory := filepath.Join(sd.Root(), "guest-workspaces")
	if err = statedir.EnsurePrivateDirectory(directory); err != nil {
		t.Fatal(err)
	}
	name := sha256.Sum256([]byte(ref))
	data, _ = json.Marshal(GuestWorkspaceRecord{Target: string(ref), ProvisioningID: "synthetic-original"})
	if err = writeGuestPrivateFixture(t, filepath.Join(directory, hex.EncodeToString(name[:])+".json"), data); err != nil {
		t.Fatal(err)
	}
	keys := &effectiveGuestRoute{KeyProvider: guestKeyProvider{targets: targets, inner: local}}
	provider := guestConfigurationProvider{server: &Server{stateDir: sd, authStore: store, targetService: targets}, keys: keys}
	return provider, keys, caller, config
}

type effectiveGuestRoute struct {
	guestssh.KeyProvider
	user, endpoint string
}

func (k *effectiveGuestRoute) GetGuestUser(ctx context.Context, ref domain.MachineRef) (string, error) {
	if k.user != "" {
		return k.user, nil
	}
	return k.KeyProvider.GetGuestUser(ctx, ref)
}
func (k *effectiveGuestRoute) GetGuestEndpoint(ctx context.Context, ref domain.MachineRef) (string, error) {
	if k.endpoint != "" {
		return k.endpoint, nil
	}
	return k.KeyProvider.GetGuestEndpoint(ctx, ref)
}

func TestGuestWorkspaceRejectsUntrustedMetadata(t *testing.T) {
	ref := domain.MachineRef("local:11111111-1111-4111-8111-111111111111")
	for _, data := range []string{`{"target":"` + string(ref) + `","provisioning_id":"x","path":"untrusted"}`, `{"target":"` + string(ref) + `","provisioning_id":""}`, `{}`, `{} {}`} {
		if _, err := decodeGuestWorkspace([]byte(data), ref); !errors.Is(err, app.ErrGuestAuthority) {
			t.Fatalf("untrusted metadata accepted: %s %v", data, err)
		}
	}
}

// Test files use the same private creation guard as server-owned records.
func writeGuestPrivateFixture(t *testing.T, path string, data []byte) error {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	security := target.NewPrivatePathSecurity()
	if err = security.ProtectNewFile(t.Context(), path); err != nil {
		return err
	}
	if err = security.ValidateFile(t.Context(), path); err != nil {
		return err
	}
	_, err = file.Write(data)
	return err
}
