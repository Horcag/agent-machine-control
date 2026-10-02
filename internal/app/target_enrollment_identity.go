package app

import (
	"context"

	"github.com/Horcag/agent-machine-control/internal/target"
)

// EnrollmentIdentity binds a desktop lab grant to the current protected enrollment.
func (s *TargetService) EnrollmentIdentity(ctx context.Context, canonical string) (string, error) {
	value, identity, err := s.store.EnrollmentIdentity(ctx)
	if err != nil {
		return "", err
	}
	if value.Locator.String() != canonical {
		return "", target.ErrDifferentTarget
	}
	return identity, nil
}
