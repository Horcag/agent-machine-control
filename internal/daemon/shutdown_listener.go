package daemon

import (
	"errors"
	"fmt"
	"net"
)

func (s *Server) closeAdmissionListener() error {
	if s.listener == nil {
		return nil
	}
	if err := s.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("daemon: listener close failed: %w", err)
	}
	return nil
}
