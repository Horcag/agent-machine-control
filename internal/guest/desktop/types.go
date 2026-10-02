// Package desktop operates exclusively inside an enrolled Windows guest.
// Application services own actor, target, approval, audit and receipt authority.
package desktop

import "github.com/Horcag/agent-machine-control/internal/domain"

type Request = domain.DesktopRequest
type Response = domain.DesktopResponse
