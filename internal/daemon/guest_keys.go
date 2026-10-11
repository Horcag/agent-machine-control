package daemon

import (
	"context"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// guestKeyProvider maps only protected local canonical identities to the existing
// raw provider GUID boundary. Session and direct recovery key routing is unchanged.
type guestKeyProvider struct {
	targets *app.TargetService
	inner   guestssh.KeyProvider
}

func (p guestKeyProvider) target(ctx context.Context, target domain.MachineRef) (domain.MachineRef, error) {
	locator, err := domain.ParseMachineLocator(string(target))
	if err != nil || locator.HostID != domain.LocalHostID {
		return "", app.ErrGuestAuthority
	}
	if _, err = p.targets.EnrollmentIdentity(ctx, locator.String()); err != nil {
		return "", err
	}
	return domain.MachineRef(locator.VMID), nil
}
func (p guestKeyProvider) GetMachineConfig(target domain.MachineRef) (*guestssh.MachineSSHConfig, error) {
	return p.GetMachineConfigContext(context.Background(), target)
}
func (p guestKeyProvider) GetMachineConfigContext(ctx context.Context, target domain.MachineRef) (*guestssh.MachineSSHConfig, error) {
	guid, err := p.target(ctx, target)
	if err != nil {
		return nil, err
	}
	if inner, ok := p.inner.(interface {
		GetMachineConfigContext(context.Context, domain.MachineRef) (*guestssh.MachineSSHConfig, error)
	}); ok {
		return inner.GetMachineConfigContext(ctx, guid)
	}
	return p.inner.GetMachineConfig(guid)
}
func (p guestKeyProvider) GetClientSigner(ctx context.Context, target domain.MachineRef) (gossh.Signer, error) {
	guid, err := p.target(ctx, target)
	if err != nil {
		return nil, err
	}
	return p.inner.GetClientSigner(ctx, guid)
}
func (p guestKeyProvider) GetHostKeyCallback(ctx context.Context, target domain.MachineRef) (gossh.HostKeyCallback, error) {
	guid, err := p.target(ctx, target)
	if err != nil {
		return nil, err
	}
	return p.inner.GetHostKeyCallback(ctx, guid)
}
func (p guestKeyProvider) GetGuestUser(ctx context.Context, target domain.MachineRef) (string, error) {
	guid, err := p.target(ctx, target)
	if err != nil {
		return "", err
	}
	return p.inner.GetGuestUser(ctx, guid)
}
func (p guestKeyProvider) GetGuestEndpoint(ctx context.Context, target domain.MachineRef) (string, error) {
	guid, err := p.target(ctx, target)
	if err != nil {
		return "", err
	}
	return p.inner.GetGuestEndpoint(ctx, guid)
}

type guestTargetResolver struct{ service *app.TargetService }

func (r guestTargetResolver) ResolveTarget(ctx context.Context, reference string) (app.TargetResolution, error) {
	return r.service.ResolveGuestTarget(ctx, reference)
}
