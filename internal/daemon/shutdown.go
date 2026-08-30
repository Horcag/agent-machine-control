package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
)

// Shutdown gracefully stops the daemon and removes the endpoint and lock files.
func (s *Server) Shutdown(ctx context.Context) error {
	s.TriggerShutdown()
	listenerErr := s.closeAdmissionListener()
	managerErr := s.shutdownManagers(ctx)

	// Terminal reporting errors do not represent live owned work. Continue
	// best-effort external teardown once every manager reports drained.
	if !s.managersDrained() {
		return errors.Join(ErrShutdownIncomplete, listenerErr, managerErr)
	}

	return errors.Join(
		listenerErr,
		managerErr,
		s.closeEventHub(),
		s.shutdownHTTPServer(ctx),
		s.removeEndpointOwnership(),
		s.releaseSingletonOwnership(),
		s.readServeError(),
	)
}

func (s *Server) shutdownManagers(ctx context.Context) error {
	var operationErr, sessionErr error
	if s.opMgr != nil {
		if err := s.opMgr.Shutdown(ctx); err != nil {
			operationErr = fmt.Errorf("daemon: operations manager shutdown error: %w", err)
		}
	}
	if s.sessionMgr != nil {
		if err := s.sessionMgr.Shutdown(ctx); err != nil {
			sessionErr = fmt.Errorf("daemon: session manager shutdown error: %w", err)
		}
	}
	return errors.Join(operationErr, sessionErr)
}

func (s *Server) closeEventHub() error {
	if s.eventHub == nil {
		return nil
	}
	if err := s.eventHub.Close(); err != nil {
		return fmt.Errorf("daemon: event hub close error: %w", err)
	}
	return nil
}

func (s *Server) shutdownHTTPServer(ctx context.Context) error {
	if s.shutdownHTTP == nil {
		return nil
	}
	if err := s.shutdownHTTP(ctx); err != nil {
		return errors.Join(fmt.Errorf("daemon: http server shutdown failed: %w", err), s.forceCloseHTTPServer())
	}
	return nil
}

func (s *Server) forceCloseHTTPServer() error {
	if s.closeHTTP == nil {
		return nil
	}
	err := s.closeHTTP()
	if err == nil || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return fmt.Errorf("daemon: forced http server close failed: %w", err)
}

func (s *Server) removeEndpointOwnership() error {
	return RemoveEndpointFileIfOwned(s.stateDir.DaemonDir(), s.pid, s.runtimeID, s.startTime)
}

func (s *Server) releaseSingletonOwnership() error {
	if s.singletonLock == nil {
		return nil
	}
	return s.singletonLock.Release()
}

func (s *Server) readServeError() error {
	s.serveErrMu.Lock()
	defer s.serveErrMu.Unlock()
	return s.serveErr
}

func (s *Server) managersDrained() bool {
	operationsDrained := s.opMgr == nil || s.opMgr.Drained()
	sessionsDrained := s.sessionMgr == nil || s.sessionMgr.Drained()
	return operationsDrained && sessionsDrained
}
