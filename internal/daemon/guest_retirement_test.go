package daemon

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"github.com/Horcag/agent-machine-control/internal/auth"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
	"github.com/Horcag/agent-machine-control/internal/guest/ssh/fakeserver"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
	gossh "golang.org/x/crypto/ssh"
)

type retirementFakeRunner struct {
	run   func([]byte) ([]byte, error)
	calls int
	t     *testing.T
}

func (r *retirementFakeRunner) RunCommand(_ context.Context, target domain.MachineRef, command string, input []byte, limit int) ([]byte, error) {
	r.calls++
	if target != "local:11111111-1111-4111-8111-111111111111" || !strings.HasPrefix(command, "powershell.exe -NoLogo -NoProfile -NonInteractive -EncodedCommand ") || limit != 16*1024 {
		r.t.Fatal("observation escaped original target or fixed bounded command")
	}
	reader := bytes.NewReader(input)
	var size uint32
	if binary.Read(reader, binary.BigEndian, &size) != nil || size > 16*1024 {
		r.t.Fatal("retirement identity framing invalid")
	}
	identity := make([]byte, size)
	if _, err := io.ReadFull(reader, identity); err != nil {
		r.t.Fatal(err)
	}
	if binary.Read(reader, binary.BigEndian, &size) != nil || int(size) != len(guestRetirementScript) {
		r.t.Fatal("retirement source framing invalid")
	}
	source := make([]byte, size)
	if _, err := io.ReadFull(reader, source); err != nil || string(source) != guestRetirementScript || reader.Len() != 0 {
		r.t.Fatal("retirement source or trailing input changed")
	}
	return r.run(identity)
}
func TestGuestRetirementExactNativeObservation(t *testing.T) {
	original := stream.WorkerIdentity{PID: 1234, BirthTicks: 638000000000000001, Image: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, SID: "S-1-5-21-1000"}
	for _, status := range []string{"live", "missing", "reused", "exited", "denied", "malformed", "mismatch"} {
		t.Run(status, func(t *testing.T) {
			runner := &retirementFakeRunner{t: t, run: func(input []byte) ([]byte, error) { return retirementFixtureResponse(t, input, original, status) }}

			provider := guestConfigurationProvider{retirementRunner: runner, retirementCurrent: func(context.Context, domain.MachineRef, domain.ActorContext) (app.GuestConfiguration, error) {
				return app.GuestConfiguration{Generation: "original-generation"}, nil
			}}
			absent, err := provider.ConfirmGuestRetirement(t.Context(), "local:11111111-1111-4111-8111-111111111111", "operator:synthetic", "original-generation", original)
			expected := status == "missing" || status == "reused" || status == "exited"
			if absent != expected || (expected && err != nil) || runner.calls != 1 {
				t.Fatalf("absent=%v error=%v calls=%d", absent, err, runner.calls)
			}
		})
	}
}
func TestGuestRetirementRejectsGenerationDrift(t *testing.T) {
	for _, changeAt := range []int{1, 2, 3} {
		t.Run(strconv.Itoa(changeAt), func(t *testing.T) {
			calls := 0
			original := stream.WorkerIdentity{PID: 12, BirthTicks: 100, Image: "synthetic-image", SID: "synthetic-sid"}
			runner := &retirementFakeRunner{t: t, run: func([]byte) ([]byte, error) {
				return json.Marshal(guestRetirementObservation{Original: original, Status: "missing", Observed: stream.WorkerIdentity{PID: original.PID}})
			}}
			provider := guestConfigurationProvider{retirementRunner: runner, retirementCurrent: func(context.Context, domain.MachineRef, domain.ActorContext) (app.GuestConfiguration, error) {
				calls++
				generation := "original"
				if calls >= changeAt {
					generation = "changed"
				}
				return app.GuestConfiguration{Generation: generation}, nil
			}}
			absent, err := provider.ConfirmGuestRetirement(t.Context(), "local:11111111-1111-4111-8111-111111111111", "operator:synthetic", "original", original)
			if absent || !errors.Is(err, app.ErrGuestAuthority) {
				t.Fatalf("generation drift accepted: %v %v", absent, err)
			}
			if changeAt < 3 && runner.calls != 0 {
				t.Fatal("dispatched after authority drift")
			}
		})
	}
}

func retirementFixtureResponse(t *testing.T, input []byte, original stream.WorkerIdentity, status string) ([]byte, error) {
	t.Helper()

	var expected stream.WorkerIdentity
	if json.Unmarshal(input, &expected) != nil || expected != original {
		t.Fatal("original process binding missing")
	}
	if status == "denied" {
		return nil, errors.New("denied")
	}
	if status == "malformed" {
		return []byte("not-json"), nil
	}
	observed := original
	nativeStatus := status
	switch status {
	case "missing":
		observed = stream.WorkerIdentity{PID: original.PID}
	case "reused":
		observed.BirthTicks++
	case "mismatch":
		nativeStatus = "exited"
		observed.SID = "S-1-5-21-2000"
	}
	return json.Marshal(guestRetirementObservation{Original: original, Status: nativeStatus, Observed: observed})

}

func TestGuestRetirementUsesFreshOperatorAndAgentAuthority(t *testing.T) {
	provider, _, caller, _ := newGuestConfigurationFixture(t)
	ref := domain.MachineRef("local:11111111-1111-4111-8111-111111111111")
	configuration, err := provider.Current(t.Context(), ref, *caller)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.retirementCommandRunner(t.Context(), ref, *caller, configuration.Generation); err != nil {
		t.Fatalf("effective default snapshot rejected: %v", err)
	}
	original := stream.WorkerIdentity{PID: 1234, BirthTicks: 638000000000000001, Image: "synthetic-image", SID: "synthetic-sid"}
	runner := &retirementFakeRunner{t: t, run: func(input []byte) ([]byte, error) { return retirementFixtureResponse(t, input, original, "missing") }}
	provider.retirementRunner = runner
	absent, err := provider.ConfirmGuestRetirement(t.Context(), ref, caller.EffectiveActor, configuration.Generation, original)
	if !absent || err != nil || runner.calls != 1 {
		t.Fatalf("fresh original authority: %v %v calls=%d", absent, err, runner.calls)
	}
	agent, err := provider.retirementActor(t.Context(), auth.AgentMCPPrincipal)
	if err != nil || agent.EffectiveActor != auth.AgentMCPPrincipal {
		t.Fatalf("agent principal resolution: %+v %v", agent, err)
	}
	if _, err = provider.retirementActor(t.Context(), "operator:foreign"); !errors.Is(err, app.ErrGuestAuthority) {
		t.Fatalf("foreign actor adopted: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = provider.retirementActor(ctx, caller.EffectiveActor); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled authority resolved: %v", err)
	}
	path := filepath.Join(provider.server.stateDir.AuthDir(), auth.OperatorTokenFileName)
	if err = os.WriteFile(path, []byte(strings.Repeat("a", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	absent, err = provider.ConfirmGuestRetirement(t.Context(), ref, caller.EffectiveActor, configuration.Generation, original)
	if absent || !errors.Is(err, app.ErrGuestAuthority) || runner.calls != 1 {
		t.Fatalf("revocation did not block observation: %v %v calls=%d", absent, err, runner.calls)
	}
}

func TestGuestFrozenRouteRejectsMixedHostPinABA(t *testing.T) {
	provider, keys, caller, _ := newGuestConfigurationFixture(t)
	ref := domain.MachineRef("local:11111111-1111-4111-8111-111111111111")
	signer, err := keys.GetClientSigner(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	other, err := fakeserver.New(fakeserver.ModeEcho, signer.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	keys.endpoint = other.Addr()
	mixed := &mixedGuestPinKeys{KeyProvider: keys, pin: other.HostKeyPin()}
	provider.keys = mixed
	before, err := provider.Current(t.Context(), ref, *caller)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := freezeGuestKeys(t.Context(), mixed, ref)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := provider
	snapshot.keys = frozen
	after, err := snapshot.Current(t.Context(), ref, *caller)
	if err != nil || after.Generation != before.Generation {
		t.Fatalf("fixture did not recreate A/B/A generation: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	channel, err := guestssh.NewTransport(frozen).OpenDuplex(ctx, ref, "synthetic-observation")
	if channel != nil {
		_ = channel.Close()
	}
	if err == nil || mixed.accepted != 1 {
		t.Fatalf("mixed-pin route was admitted or B callback not exercised: %v accepted=%d", err, mixed.accepted)
	}
}

type mixedGuestPinKeys struct {
	guestssh.KeyProvider
	pin      string
	accepted int
}

func (k *mixedGuestPinKeys) GetHostKeyCallback(context.Context, domain.MachineRef) (gossh.HostKeyCallback, error) {
	return func(_ string, _ net.Addr, key gossh.PublicKey) error {
		sum := sha256.Sum256(key.Marshal())
		if base64.StdEncoding.EncodeToString(sum[:]) != k.pin {
			return domain.ErrHostKeyMismatch
		}
		k.accepted++
		return nil
	}, nil
}
func TestGuestFrozenPinPreservesProviderPolicy(t *testing.T) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(signer.PublicKey().Marshal())
	pin := base64.StdEncoding.EncodeToString(sum[:])
	denied := errors.New("synthetic provider denial")
	callback, err := frozenGuestHostKeyCallback(pin, func(string, net.Addr, gossh.PublicKey) error { return denied })
	if err != nil {
		t.Fatal(err)
	}
	if err = callback("synthetic", nil, signer.PublicKey()); !errors.Is(err, denied) {
		t.Fatalf("provider policy bypassed: %v", err)
	}
	callback, err = frozenGuestHostKeyCallback(" "+pin+" ", func(string, net.Addr, gossh.PublicKey) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err = callback("synthetic", nil, signer.PublicKey()); err != nil {
		t.Fatalf("matching policy and pin rejected: %v", err)
	}
	if _, err = frozenGuestHostKeyCallback("", callback); !errors.Is(err, domain.ErrMissingHostKeyPin) {
		t.Fatalf("missing pin accepted: %v", err)
	}
}
