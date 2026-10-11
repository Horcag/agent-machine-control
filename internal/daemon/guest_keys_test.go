package daemon

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
	"github.com/Horcag/agent-machine-control/internal/guest/ssh/fakeserver"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/Horcag/agent-machine-control/internal/target"
	gossh "golang.org/x/crypto/ssh"
)

func guestEnrolledKeyFixture(t *testing.T) (*statedir.StateDir, *app.TargetService, guestssh.KeyProvider, *fakeserver.FakeSSHServer) {
	t.Helper()
	sd, err := statedir.Resolve(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	if err = sd.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	signer := writeGuestFixtureClientKey(t, sd)
	server, err := fakeserver.New(fakeserver.ModeEcho, signer.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	guid := "11111111-1111-4111-8111-111111111111"
	directory := filepath.Join(sd.MachinesDir(), guid)
	if err = statedir.EnsurePrivateDirectory(directory); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(guestssh.MachineSSHConfig{Endpoint: server.Addr(), User: "synthetic-user", DefaultKeyAlias: "default", PinnedHostKeySHA256: server.HostKeyPin()})
	if err = writeGuestPrivateFixture(t, filepath.Join(directory, "config.json"), data); err != nil {
		t.Fatal(err)
	}
	store, err := target.NewStore(sd.TargetsDir())
	if err != nil {
		t.Fatal(err)
	}
	locator, err := domain.ParseMachineLocator("local:" + guid)
	if err != nil {
		t.Fatal(err)
	}
	value, err := target.NewDefault(locator, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Save(t.Context(), value); err != nil {
		t.Fatal(err)
	}
	inventory, err := app.NewTrustedInventory(nil)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := app.NewTargetService(inventory, store)
	if err != nil {
		t.Fatal(err)
	}
	return sd, targets, guestssh.NewLocalKeyProvider(sd), server
}
func TestGuestCanonicalLocalKeyProviderPinnedDuplex(t *testing.T) {
	_, targets, local, server := guestEnrolledKeyFixture(t)
	keys := guestKeyProvider{targets: targets, inner: local}
	targetRef := domain.MachineRef("local:11111111-1111-4111-8111-111111111111")
	config, err := keys.GetMachineConfigContext(t.Context(), targetRef)
	if err != nil || config.Endpoint != server.Addr() {
		t.Fatalf("raw GUID config mapping: %+v %v", config, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	duplex, err := guestssh.NewTransport(keys).OpenDuplex(ctx, targetRef, "synthetic-observation")
	if err != nil {
		t.Fatal(err)
	}
	defer duplex.Close()
	prefix := make([]byte, len("PS C:\\> "))
	if _, err = io.ReadFull(duplex, prefix); err != nil {
		t.Fatal(err)
	}
	payload := []byte{0, 255, 128, 1}
	if _, err = duplex.Write(payload); err != nil {
		t.Fatal(err)
	}
	received := make([]byte, len(payload))
	if _, err = io.ReadFull(duplex, received); err != nil || !bytes.Equal(received, payload) {
		t.Fatalf("binary pinned route: %v %v", received, err)
	}
	if _, err = keys.GetGuestEndpoint(ctx, "local:22222222-2222-4222-8222-222222222222"); err == nil {
		t.Fatal("foreign canonical identity reached provider")
	}
	if _, err = keys.GetGuestEndpoint(ctx, "11111111-1111-4111-8111-111111111111"); err == nil {
		t.Fatal("unvalidated raw GUID bypassed protected mapping")
	}
}

func writeGuestFixtureClientKey(t *testing.T, sd *statedir.StateDir) gossh.Signer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	block, err := gossh.MarshalPrivateKey(key, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = writeGuestFixtureKeyMaterial(t, sd, pem.EncodeToMemory(block)); err != nil {
		t.Fatal(err)
	}

	return signer
}
