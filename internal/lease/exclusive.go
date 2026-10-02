package lease

import (
	"context"
	"errors"
)

// WithExclusiveLock fences one bounded critical section across processes. It uses
// verified process ownership, so a living holder is never displaced by elapsed time.
// The caller must prove that the manager's directory is private before entering.
func (m *Manager) WithExclusiveLock(ctx context.Context, key string, fn func() error) error {
	if m == nil || fn == nil {
		return errors.New("lease: exclusive lock requires a manager and operation")
	}
	return m.withLock(ctx, key, fn)
}
