package app

import "context"

// WithEnrollmentFence shares the publication fence used by every target Store writer.
func (s *TargetService) WithEnrollmentFence(ctx context.Context, fn func() error) error {
	return s.store.WithEnrollmentFence(ctx, fn)
}
