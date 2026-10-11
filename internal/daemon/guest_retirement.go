package daemon

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"unicode/utf16"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
	gossh "golang.org/x/crypto/ssh"
)

//go:embed guest_retirement.ps1
var guestRetirementScript string

type guestCommandRunner interface {
	RunCommand(context.Context, domain.MachineRef, string, []byte, int) ([]byte, error)
}
type guestRetirementObservation struct {
	Original stream.WorkerIdentity `json:"original"`
	Status   string                `json:"status"`
	Observed stream.WorkerIdentity `json:"observed"`
}

// ConfirmGuestRetirement observes only the original process on the original
// enrolled route. No process is stopped, elevated, or inferred absent from SSH EOF.
func (p *guestConfigurationProvider) ConfirmGuestRetirement(ctx context.Context, target domain.MachineRef, actor domain.ActorID, generation string, original stream.WorkerIdentity) (bool, error) {
	caller, err := p.retirementActor(ctx, actor)
	if err != nil {
		return false, err
	}
	before, err := p.retirementConfiguration(ctx, target, caller)
	if err != nil || before.Generation != generation {
		return false, app.ErrGuestAuthority
	}
	runner, err := p.retirementCommandRunner(ctx, target, caller, generation)
	if err != nil {
		return false, err
	}
	// Freeze credentials and route, then revalidate before dispatch: changed state
	// cannot silently redirect an original-process observation to a new endpoint.
	ready, err := p.retirementConfiguration(ctx, target, caller)
	if err != nil || ready.Generation != generation {
		return false, app.ErrGuestAuthority
	}
	input, err := json.Marshal(original)
	if err != nil {
		return false, err
	}
	data, err := runner.RunCommand(ctx, target, encodedGuestRetirementCommand(), input, 16*1024)
	if err != nil {
		return false, app.ErrGuestUncertain
	}
	after, err := p.retirementConfiguration(ctx, target, caller)
	if err != nil || after.Generation != generation {
		return false, app.ErrGuestAuthority
	}
	return decodeGuestRetirement(data, original)
}
func (p *guestConfigurationProvider) retirementConfiguration(ctx context.Context, target domain.MachineRef, caller domain.ActorContext) (app.GuestConfiguration, error) {
	if p.retirementCurrent != nil {
		return p.retirementCurrent(ctx, target, caller)
	}
	return p.Current(ctx, target, caller)
}
func (p *guestConfigurationProvider) retirementActor(ctx context.Context, actor domain.ActorID) (domain.ActorContext, error) {
	if p.retirementCurrent != nil {
		return domain.ActorContext{AuthenticatedCaller: actor, EffectiveActor: actor}, nil
	}
	kind := auth.TokenTypeOperator
	if actor == auth.AgentMCPPrincipal {
		kind = auth.TokenTypeAgentMCP
	}
	token, err := auth.ReadTokenFile(p.server.stateDir.AuthDir(), kind)
	if err != nil {
		return domain.ActorContext{}, app.ErrGuestAuthority
	}
	caller, _, ok := p.server.authStore.Authenticate(token)
	if !ok || caller == nil || caller.EffectiveActor != actor {
		return domain.ActorContext{}, app.ErrGuestAuthority
	}
	current, err := p.currentActor(ctx, *caller)
	if err != nil {
		return domain.ActorContext{}, err
	}
	return *current, nil
}
func (p *guestConfigurationProvider) retirementCommandRunner(ctx context.Context, target domain.MachineRef, caller domain.ActorContext, generation string) (guestCommandRunner, error) {
	if p.retirementRunner != nil {
		return p.retirementRunner, nil
	}
	frozen, err := freezeGuestKeys(ctx, p.keys, target)
	if err != nil {
		return nil, err
	}
	snapshot := *p
	snapshot.keys = frozen
	configuration, err := snapshot.Current(ctx, target, caller)
	if err != nil || configuration.Generation != generation {
		return nil, app.ErrGuestAuthority
	}
	return guestssh.NewTransport(frozen), nil
}
func encodedGuestRetirementCommand() string {
	units := utf16.Encode([]rune(guestRetirementScript))
	data := make([]byte, len(units)*2)
	for i, unit := range units {
		binary.LittleEndian.PutUint16(data[i*2:], unit)
	}
	return "powershell.exe -NoLogo -NoProfile -NonInteractive -EncodedCommand " + base64.StdEncoding.EncodeToString(data)
}
func decodeGuestRetirement(data []byte, original stream.WorkerIdentity) (bool, error) {
	if len(data) > 16*1024 || rejectDuplicateJSONKeys(data) != nil {
		return false, app.ErrGuestUncertain
	}
	var observation guestRetirementObservation
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var extra any
	if decoder.Decode(&observation) != nil || decoder.Decode(&extra) != io.EOF || observation.Original != original || observation.Observed.PID != original.PID {
		return false, app.ErrGuestUncertain
	}
	switch observation.Status {
	case "missing":
		return observation.Observed.BirthTicks == 0 && observation.Observed.Image == "" && observation.Observed.SID == "", nil
	case "reused":
		return observation.Observed.BirthTicks > 0 && observation.Observed.BirthTicks != original.BirthTicks, nil
	case "live", "exited":
		if observation.Observed != original {
			return false, app.ErrGuestUncertain
		}
		return observation.Status == "exited", nil
	default:
		return false, app.ErrGuestUncertain
	}
}

// frozenGuestKeys retains the exact enrollment-approved route across a single
// observation. It is not a reusable credential cache or a revocation fallback.
type frozenGuestKeys struct {
	config         guestssh.MachineSSHConfig
	user, endpoint string
	signer         gossh.Signer
	callback       gossh.HostKeyCallback
}

func freezeGuestKeys(ctx context.Context, keys guestssh.KeyProvider, target domain.MachineRef) (*frozenGuestKeys, error) {
	config, err := guestMachineConfig(ctx, keys, target)
	if err != nil {
		return nil, err
	}
	user, err := keys.GetGuestUser(ctx, target)
	if err != nil {
		return nil, err
	}
	endpoint, err := keys.GetGuestEndpoint(ctx, target)
	if err != nil {
		return nil, err
	}
	signer, err := keys.GetClientSigner(ctx, target)
	if err != nil {
		return nil, err
	}
	callback, err := keys.GetHostKeyCallback(ctx, target)
	if err != nil {
		return nil, err
	}
	return &frozenGuestKeys{config: *config, user: user, endpoint: endpoint, signer: signer, callback: callback}, nil
}
func (k *frozenGuestKeys) GetMachineConfig(domain.MachineRef) (*guestssh.MachineSSHConfig, error) {
	configuration := k.config
	return &configuration, nil
}
func (k *frozenGuestKeys) GetClientSigner(context.Context, domain.MachineRef) (gossh.Signer, error) {
	return k.signer, nil
}
func (k *frozenGuestKeys) GetHostKeyCallback(context.Context, domain.MachineRef) (gossh.HostKeyCallback, error) {
	return k.callback, nil
}
func (k *frozenGuestKeys) GetGuestUser(context.Context, domain.MachineRef) (string, error) {
	return k.user, nil
}
func (k *frozenGuestKeys) GetGuestEndpoint(context.Context, domain.MachineRef) (string, error) {
	return k.endpoint, nil
}
