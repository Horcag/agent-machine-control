package target

import (
	"context"
	"errors"

	"github.com/Horcag/agent-machine-control/internal/lease"
)

// WithEnrollmentFence prevents enrollment publication while a guest operation
// validates its protected enrollment identity and dispatches under that authority.
func (s *Store) WithEnrollmentFence(ctx context.Context, fn func() error) error {
	if s == nil || fn == nil {
		return errors.New("target: enrollment fence requires a store and operation")
	}
	if err := s.validateDirectory(ctx); err != nil {
		return err
	}
	return lease.NewManager(s.dir).WithExclusiveLock(ctx, ".enrollment-dispatch", fn)
}

func (s *Store) withPublicationFence(ctx context.Context, prepare bool, fn func() (Publication, error)) (Publication, error) {
	if prepare {
		if err := s.prepareDirectory(ctx); err != nil {
			return s.pendingPublicationFailure(err)
		}
	}
	var publication Publication
	entered := false
	err := s.WithEnrollmentFence(ctx, func() error {
		entered = true
		var err error
		publication, err = fn()
		return err
	})
	if err != nil && !entered {
		return s.pendingPublicationFailure(err)
	}
	return publication, err
}

func (s *Store) pendingPublicationFailure(err error) (Publication, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending != nil {
		return Publication{Committed: true}, errors.Join(ErrCommittedNotDurable, err)
	}
	return Publication{}, err
}
