package stream

import (
	"context"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

// acquire serializes a worker's full request lifecycle. Waiters on an evicted
// worker fail rather than creating a replacement under an old admission.
func (c *Client) acquire(ctx context.Context, key poolKey, create bool) (*worker, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, nil, ErrClosed
	}
	w := c.workers[key]
	if w == nil {
		if !create || len(c.workers) >= MaxWorkers {
			c.mu.Unlock()
			return nil, nil, ErrRequest
		}
		w = &worker{lane: make(chan struct{}, 1)}
		c.workers[key] = w
	}
	c.mu.Unlock()
	select {
	case w.lane <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	release := func() { <-w.lane }
	c.mu.Lock()
	valid := !c.closed && c.workers[key] == w
	c.mu.Unlock()
	if !valid {
		release()
		return nil, nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, nil, err
	}
	return w, release, nil
}

// Retire revokes reuse of one exact worker. Local channel closure is not proof
// of remote process absence; the app retains its native cleanup obligation.
func (c *Client) Retire(target domain.MachineRef, actor, generation string) error {
	key := poolKey{target, actor, generation}
	c.mu.Lock()
	defer c.mu.Unlock()
	w := c.workers[key]
	if w == nil {
		return nil
	}
	delete(c.workers, key)
	if w.channel != nil {
		return w.channel.Close()
	}
	return nil
}
