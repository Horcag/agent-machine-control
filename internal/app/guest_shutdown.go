package app

import (
	"context"
	"errors"
	"maps"
)

func (s *GuestService) beginGuestCall() error {
	if s == nil {
		return ErrGuestAuthority
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return ErrGuestAuthority
	}
	s.running++
	return nil
}
func (s *GuestService) endGuestCall() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running--
	if s.closing && s.running == 0 {
		close(s.drained)
	}
}

// Shutdown retires only workers created by this service. A closed channel is
// insufficient evidence of native absence; unresolved obligations remain durable.
func (s *GuestService) Shutdown(ctx context.Context) error {
	if s == nil {
		return ErrGuestAuthority
	}
	s.mu.Lock()
	if !s.closing {
		s.closing = true
		s.drained = make(chan struct{})
		if s.running == 0 {
			close(s.drained)
		}
	}
	drained := s.drained
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return errors.Join(ErrGuestUncertain, ctx.Err())
	case <-drained:
	}
	select {
	case <-ctx.Done():
		return errors.Join(ErrGuestUncertain, ctx.Err())
	case <-s.shutdownGate:
	}
	defer func() { s.shutdownGate <- struct{}{} }()
	if err := ctx.Err(); err != nil {
		return errors.Join(ErrGuestUncertain, err)
	}
	s.mu.Lock()
	if s.shutdownDone {
		s.mu.Unlock()
		return nil
	}
	workers := maps.Clone(s.workers)
	s.mu.Unlock()
	var result error
	for key, worker := range workers {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, ErrGuestUncertain, err)
		}
		result = errors.Join(result, s.retireWorkerContext(ctx, key, worker))
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(result, ErrGuestUncertain, err)
	}
	if result == nil {
		s.mu.Lock()
		s.shutdownDone = true
		s.mu.Unlock()
	}
	return result
}
