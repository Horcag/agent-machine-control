package actor

import (
	"fmt"
	"os/user"
	"strings"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

// DefaultResolver resolves the current local OS authenticated identity.
type DefaultResolver struct {
	CurrentFn func() (*user.User, error)
}

// Resolve returns the ActorContext derived from the current OS user runtime.
func (r *DefaultResolver) Resolve() (domain.ActorContext, error) {
	current := user.Current
	if r != nil && r.CurrentFn != nil {
		current = r.CurrentFn
	}
	u, err := current()
	if err != nil {
		return domain.ActorContext{}, fmt.Errorf("actor: failed to resolve current OS user: %w", err)
	}
	if u == nil || strings.TrimSpace(u.Uid) == "" {
		return domain.ActorContext{}, fmt.Errorf("actor: current OS user has empty identity")
	}

	actorID := domain.ActorID(fmt.Sprintf("user:%s", strings.TrimSpace(u.Uid)))
	if err := actorID.Validate(); err != nil {
		return domain.ActorContext{}, fmt.Errorf("actor: invalid derived actor ID: %w", err)
	}

	scopes := domain.NewScopeSet(
		"machine:read",
		"machine:write",
	)

	return domain.NewActorContext(actorID, actorID, scopes, scopes)
}
