//go:build !unix && !windows

package sessions

import (
	"errors"
	"os"
)

func openSessionStateFile(_, _ string) (*os.File, error) {
	return nil, errors.New("sessions: no-follow state reads are unsupported on this platform")
}
