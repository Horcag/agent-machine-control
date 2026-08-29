package daemon

import (
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/lease"
)

// Config holds runtime options for the amcd daemon server.
type Config struct {
	StateDir          string
	ListenAddr        string
	ShutdownTimeout   time.Duration
	Clock             func() time.Time
	LivenessChecker   lease.LivenessChecker
	IdentityProvider  lease.IdentityProvider
	PrincipalResolver auth.PrincipalResolver
	Backend           app.Backend
}
