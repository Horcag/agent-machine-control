package ssh

import (
	"fmt"
	"net"
	"time"
)

// DialFailure preserves a channel created before a later dial-finalization failure.
// The session manager owns bounded cleanup of Channel when this error is returned.
type DialFailure struct {
	Cause   error
	Channel Channel
}

func (e *DialFailure) Error() string {
	if e == nil || e.Cause == nil {
		return "ssh: dial finalization failed"
	}
	return e.Cause.Error()
}

// Unwrap exposes the original dial-finalization failure.
func (e *DialFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func finalizeDialChannel(conn net.Conn, channel Channel) (Channel, error) {
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, &DialFailure{
			Cause:   fmt.Errorf("ssh: failed to clear connection deadline: %w", err),
			Channel: channel,
		}
	}
	return channel, nil
}
