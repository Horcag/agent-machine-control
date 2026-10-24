package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
	"github.com/Horcag/agent-machine-control/internal/target"
)

// GuestWorkspaceRecord is original provisioning evidence, installed separately
// by an authorized operator. The daemon never observes or adopts a guest path.
type GuestWorkspaceRecord struct {
	Target         string              `json:"target"`
	ProvisioningID string              `json:"provisioning_id"`
	Workspace      stream.RootIdentity `json:"workspace"`
}
type guestConfigurationProvider struct {
	server            *Server
	keys              guestssh.KeyProvider
	retirementRunner  guestCommandRunner
	retirementCurrent func(context.Context, domain.MachineRef, domain.ActorContext) (app.GuestConfiguration, error)
}

func (p *guestConfigurationProvider) Current(ctx context.Context, targetRef domain.MachineRef, caller domain.ActorContext) (app.GuestConfiguration, error) {
	var zero app.GuestConfiguration
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	current, err := p.currentActor(ctx, caller)
	if err != nil {
		return zero, err
	}
	enrollment, err := p.server.targetService.EnrollmentIdentity(ctx, string(targetRef))
	if err != nil {
		return zero, err
	}
	config, err := guestMachineConfig(ctx, p.keys, targetRef)
	if err != nil {
		return zero, err
	}
	user, err := p.keys.GetGuestUser(ctx, targetRef)
	if err != nil {
		return zero, err
	}
	endpoint, err := p.keys.GetGuestEndpoint(ctx, targetRef)
	if err != nil {
		return zero, err
	}
	signer, err := p.keys.GetClientSigner(ctx, targetRef)
	if err != nil {
		return zero, err
	}
	if _, err = p.keys.GetHostKeyCallback(ctx, targetRef); err != nil {
		return zero, err
	}
	record, err := p.workspaceRecord(ctx, targetRef)
	if err != nil {
		return zero, err
	}
	authority, err := json.Marshal(struct {
		Record         GuestWorkspaceRecord
		Config         *guestssh.MachineSSHConfig
		User, Endpoint string
		Actor          domain.ActorContext
		Key            []byte
		TokenState     [][]byte
		Enrollment     string
	}{record, config, user, endpoint, *current, signer.PublicKey().Marshal(), p.server.authStore.ActiveBearerSecrets(), enrollment})
	if err != nil {
		return zero, err
	}
	generation := sha256.Sum256(authority)
	return app.GuestConfiguration{Generation: hex.EncodeToString(generation[:]), Caller: *current, WorkspaceIdentity: record.Workspace}, nil
}

func (p *guestConfigurationProvider) currentActor(ctx context.Context, caller domain.ActorContext) (*domain.ActorContext, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tokenType := auth.TokenTypeOperator
	if caller.AuthenticatedCaller == auth.AgentMCPPrincipal {
		tokenType = auth.TokenTypeAgentMCP
	}
	token, err := auth.ReadTokenFile(p.server.stateDir.AuthDir(), tokenType)
	if err != nil {
		return nil, app.ErrGuestAuthority
	}
	current, _, ok := p.server.authStore.Authenticate(token)
	if !ok || current == nil || current.AuthenticatedCaller != caller.AuthenticatedCaller || caller.IsDelegated() || !caller.CallerPermissions.IsSubsetOf(current.CallerPermissions) || !caller.EffectivePermissions.IsSubsetOf(current.EffectivePermissions) {
		return nil, app.ErrGuestAuthority
	}
	return current, nil
}
func (p *guestConfigurationProvider) workspaceRecord(ctx context.Context, targetRef domain.MachineRef) (GuestWorkspaceRecord, error) {
	var zero GuestWorkspaceRecord
	directory := filepath.Join(p.server.stateDir.Root(), "guest-workspaces")
	security := target.NewPrivatePathSecurity()
	var err error
	if err = security.ValidateDir(ctx, directory); err != nil {
		return zero, app.ErrGuestWorkspaceUnprovisioned
	}
	// A digest of the canonical target avoids path interpretation entirely.
	name := sha256.Sum256([]byte(targetRef))
	path := filepath.Join(directory, hex.EncodeToString(name[:])+".json")
	if err = security.ValidateFile(ctx, path); err != nil {
		return zero, app.ErrGuestWorkspaceUnprovisioned
	}
	original, err := os.Lstat(path)
	if err != nil {
		return zero, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return zero, err
	}
	defer root.Close()
	file, err := root.Open(filepath.Base(path))
	if err != nil {
		return zero, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(original, opened) {
		return zero, app.ErrGuestAuthority
	}
	if err = security.ValidateFile(ctx, path); err != nil {
		return zero, err
	}
	data, err := io.ReadAll(io.LimitReader(file, GuestMetadataLimit+1))
	if err != nil || len(data) > GuestMetadataLimit {
		return zero, app.ErrGuestAuthority
	}
	if err = rejectDuplicateJSONKeys(data); err != nil {
		return zero, app.ErrGuestAuthority
	}
	return decodeGuestWorkspace(data, targetRef)
}
func decodeGuestWorkspace(data []byte, targetRef domain.MachineRef) (GuestWorkspaceRecord, error) {
	var zero GuestWorkspaceRecord
	var record GuestWorkspaceRecord
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var extra any
	if dec.Decode(&record) != nil || dec.Decode(&extra) != io.EOF || record.Target != string(targetRef) || record.ProvisioningID == "" {
		return zero, app.ErrGuestAuthority
	}
	return record, nil
}

func (s *Server) initializeGuestService(sessionSvc *app.SessionService, targetService *app.TargetService, keyProvider guestssh.KeyProvider) {
	keyProvider = guestKeyProvider{targets: targetService, inner: keyProvider}
	guestConfig := s.cfg.GuestConfigProvider
	if guestConfig == nil {
		guestConfig = &guestConfigurationProvider{server: s, keys: keyProvider}
	}
	guestTransport := s.cfg.GuestTransport
	if guestTransport == nil {
		guestTransport = newDefaultGuestTransport(s, keyProvider)
	}
	s.guestService = app.NewGuestService(sessionSvc, guestTargetResolver{service: targetService}, guestConfig, guestTransport, app.NewGuestStore(filepath.Join(s.stateDir.Root(), "guest-operations")))
}

func guestMachineConfig(ctx context.Context, keys guestssh.KeyProvider, targetRef domain.MachineRef) (*guestssh.MachineSSHConfig, error) {
	if provider, ok := keys.(interface {
		GetMachineConfigContext(context.Context, domain.MachineRef) (*guestssh.MachineSSHConfig, error)
	}); ok {
		return provider.GetMachineConfigContext(ctx, targetRef)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return keys.GetMachineConfig(targetRef)
}
