package ssh_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/guest/ssh"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	gossh "golang.org/x/crypto/ssh"
)

func generateTestEd25519PEM(t *testing.T) ([]byte, gossh.PublicKey, string) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	sshPub, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("failed to create ssh pubkey: %v", err)
	}

	sum := sha256.Sum256(sshPub.Marshal())
	pin := base64.StdEncoding.EncodeToString(sum[:])

	privBytes, err := gossh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("failed to marshal private key: %v", err)
	}

	pemBlock := pem.EncodeToMemory(privBytes)
	return pemBlock, sshPub, pin
}

const testTargetGUID = "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

func setupTestStateDirWithKey(t *testing.T, pin string) (*statedir.StateDir, []byte, gossh.PublicKey) {
	tempDir := t.TempDir()
	sd, err := statedir.Resolve(tempDir)
	if err != nil {
		t.Fatalf("failed to resolve state dir: %v", err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatalf("failed to ensure dirs: %v", err)
	}

	pemData, pubKey, generatedPin := generateTestEd25519PEM(t)
	if pin == "" {
		pin = generatedPin
	}

	// Write key file
	keyPath := filepath.Join(sd.KeysDir(), "default.key")
	if err := os.WriteFile(keyPath, pemData, 0600); err != nil {
		t.Fatalf("failed to write key file: %v", err)
	}

	// Write machine config
	machDir := filepath.Join(sd.MachinesDir(), testTargetGUID)
	if err := os.MkdirAll(machDir, 0700); err != nil {
		t.Fatalf("failed to create machine dir: %v", err)
	}

	cfg := ssh.MachineSSHConfig{
		Endpoint:                 "127.0.0.1:2222",
		User:                     "testadmin",
		DefaultKeyAlias:          "default",
		PinnedHostKeySHA256:      pin,
		ExternalEffectsContained: true,
		RollbackCheckpointID:     "chk-123",
	}
	cfgBytes, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("failed to marshal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(machDir, "config.json"), cfgBytes, 0600); err != nil {
		t.Fatalf("failed to write machine config: %v", err)
	}

	return sd, pemData, pubKey
}

func TestLocalKeyProvider_Success(t *testing.T) {
	ctx := context.Background()
	guid := testTargetGUID
	sd, _, pubKey := setupTestStateDirWithKey(t, "")

	kp := ssh.NewLocalKeyProvider(sd)

	if runtime.GOOS != "windows" {
		signer, err := kp.GetClientSigner(ctx, domain.MachineRef(guid))
		if err != nil {
			t.Fatalf("GetClientSigner failed: %v", err)
		}
		if signer == nil {
			t.Fatal("expected non-nil signer")
		}
	}

	user, err := kp.GetGuestUser(ctx, domain.MachineRef(guid))
	if err != nil || user != "testadmin" {
		t.Errorf("GetGuestUser got %q, err %v", user, err)
	}

	endpoint, err := kp.GetGuestEndpoint(ctx, domain.MachineRef(guid))
	if err != nil || endpoint != "127.0.0.1:2222" {
		t.Errorf("GetGuestEndpoint got %q, err %v", endpoint, err)
	}

	cb, err := kp.GetHostKeyCallback(ctx, domain.MachineRef(guid))
	if err != nil {
		t.Fatalf("GetHostKeyCallback failed: %v", err)
	}

	fakeAddr := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 2222}
	if err := cb("127.0.0.1:2222", fakeAddr, pubKey); err != nil {
		t.Errorf("expected host key match, got: %v", err)
	}

	mCfg, err := kp.GetMachineConfig(domain.MachineRef(guid))
	if err != nil {
		t.Fatalf("GetMachineConfig failed: %v", err)
	}
	if !mCfg.ExternalEffectsContained || mCfg.RollbackCheckpointID != "chk-123" {
		t.Errorf("unexpected machine config: %+v", mCfg)
	}
}

func TestLocalKeyProvider_HostKeyMismatch(t *testing.T) {
	ctx := context.Background()
	guid := testTargetGUID
	sd, _, _ := setupTestStateDirWithKey(t, "different-pinned-pin==")

	kp := ssh.NewLocalKeyProvider(sd)

	cb, err := kp.GetHostKeyCallback(ctx, domain.MachineRef(guid))
	if err != nil {
		t.Fatalf("GetHostKeyCallback failed: %v", err)
	}

	_, otherPub, _ := generateTestEd25519PEM(t)
	fakeAddr := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 2222}
	err = cb("127.0.0.1:2222", fakeAddr, otherPub)
	if err == nil || !errors.Is(err, domain.ErrHostKeyMismatch) {
		t.Fatalf("expected ErrHostKeyMismatch, got: %v", err)
	}
}

func TestLocalKeyProvider_MissingPin(t *testing.T) {
	ctx := context.Background()
	guid := testTargetGUID
	tempDir := t.TempDir()
	sd, _ := statedir.Resolve(tempDir)
	_ = sd.EnsureDirs()

	machDir := filepath.Join(sd.MachinesDir(), guid)
	_ = os.MkdirAll(machDir, 0700)
	cfg := ssh.MachineSSHConfig{
		Endpoint: "127.0.0.1:2222",
		User:     "admin",
	}
	data, _ := json.Marshal(cfg)
	_ = os.WriteFile(filepath.Join(machDir, "config.json"), data, 0600)

	kp := ssh.NewLocalKeyProvider(sd)
	_, err := kp.GetHostKeyCallback(ctx, domain.MachineRef(guid))
	if err == nil || !errors.Is(err, domain.ErrMissingHostKeyPin) {
		t.Fatalf("expected ErrMissingHostKeyPin, got: %v", err)
	}
}

func TestLocalKeyProvider_RejectsNonCanonicalNestedConfiguration(t *testing.T) {
	sd, err := statedir.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := sd.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	machineDir := filepath.Join(sd.MachinesDir(), testTargetGUID)
	if err := os.Mkdir(machineDir, 0700); err != nil {
		t.Fatal(err)
	}
	nested := []byte(`{"ssh":{"endpoint":"127.0.0.1:2222","host_key_pin_sha256":"synthetic"}}`)
	if err := os.WriteFile(filepath.Join(machineDir, "config.json"), nested, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ssh.NewLocalKeyProvider(sd).GetMachineConfig(domain.MachineRef(testTargetGUID)); err == nil {
		t.Fatal("nested non-canonical machine configuration was accepted")
	}
}

func TestLocalKeyProvider_SymlinkRejection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("plaintext key symlink behavior is POSIX-specific; Windows uses DPAPI and DACL tests")
	}
	ctx := context.Background()
	guid := testTargetGUID
	sd, _, _ := setupTestStateDirWithKey(t, "")

	// Create symlinked key
	realKey := filepath.Join(sd.KeysDir(), "default.key")
	symlinkKey := filepath.Join(sd.KeysDir(), "symlink.key")
	if err := os.Symlink(realKey, symlinkKey); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	// Change machine config to use symlink
	machDir := filepath.Join(sd.MachinesDir(), guid)
	cfg := ssh.MachineSSHConfig{
		Endpoint:            "127.0.0.1:2222",
		User:                "testadmin",
		DefaultKeyAlias:     "symlink",
		PinnedHostKeySHA256: "some-pin",
	}
	data, _ := json.Marshal(cfg)
	_ = os.WriteFile(filepath.Join(machDir, "config.json"), data, 0600)

	kp := ssh.NewLocalKeyProvider(sd)
	_, err := kp.GetClientSigner(ctx, domain.MachineRef(guid))
	if err == nil {
		t.Fatal("expected error for symlinked private key")
	}
}

func TestLocalKeyProvider_GettersAndErrors(t *testing.T) {
	ctx := context.Background()
	guid := testTargetGUID
	sd, _, _ := setupTestStateDirWithKey(t, "")

	kp := ssh.NewLocalKeyProvider(sd)

	// User from config
	user, err := kp.GetGuestUser(ctx, domain.MachineRef(guid))
	if err != nil || user != "testadmin" {
		t.Errorf("expected testadmin from config, got: %s (err: %v)", user, err)
	}

	// Endpoint
	ep, err := kp.GetGuestEndpoint(ctx, domain.MachineRef(guid))
	if err != nil || ep != "127.0.0.1:2222" {
		t.Errorf("expected 127.0.0.1:2222, got: %s (err: %v)", ep, err)
	}

	// Signer with default key
	if runtime.GOOS != "windows" {
		signer, err := kp.GetClientSigner(ctx, domain.MachineRef(guid))
		if err != nil || signer == nil {
			t.Fatalf("expected signer for default key, got: %v", err)
		}
	}

	// Nil stateDir error
	nilKP := ssh.NewLocalKeyProvider(nil)
	if _, err := nilKP.GetGuestEndpoint(ctx, domain.MachineRef(guid)); err == nil {
		t.Errorf("expected error on nil state dir")
	}
	if u, err := nilKP.GetGuestUser(ctx, domain.MachineRef(guid)); err != nil || u != "Administrator" {
		t.Errorf("expected Administrator on nil state dir, got %q (err: %v)", u, err)
	}
	if _, err := nilKP.GetHostKeyCallback(ctx, domain.MachineRef(guid)); err == nil {
		t.Errorf("expected error on nil state dir")
	}
	if _, err := nilKP.GetClientSigner(ctx, domain.MachineRef(guid)); err == nil {
		t.Errorf("expected error on nil state dir")
	}

	// Invalid machine GUID
	if _, err := kp.GetGuestEndpoint(ctx, "bad-guid"); err == nil {
		t.Errorf("expected error on bad machine GUID")
	}
}

func TestMockKeyProvider_Getters(t *testing.T) {
	ctx := context.Background()
	guid := testTargetGUID

	mockKP := &ssh.MockKeyProvider{
		User:     "mockuser",
		Endpoint: "10.0.0.1:22",
	}
	if u, _ := mockKP.GetGuestUser(ctx, domain.MachineRef(guid)); u != "mockuser" {
		t.Errorf("expected mockuser, got %s", u)
	}
	if ep, _ := mockKP.GetGuestEndpoint(ctx, domain.MachineRef(guid)); ep != "10.0.0.1:22" {
		t.Errorf("expected 10.0.0.1:22, got %s", ep)
	}
	defaultMock := &ssh.MockKeyProvider{}
	if u, _ := defaultMock.GetGuestUser(ctx, domain.MachineRef(guid)); u != "testuser" {
		t.Errorf("expected default testuser, got %s", u)
	}
	if ep, _ := defaultMock.GetGuestEndpoint(ctx, domain.MachineRef(guid)); ep != "127.0.0.1:22" {
		t.Errorf("expected default 127.0.0.1:22, got %s", ep)
	}
}
