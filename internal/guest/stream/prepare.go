package stream

import (
	"context"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

// Prepare starts a worker and observes its native identity without performing a
// business operation. The app must durably admit this identity before Execute.
func (c *Client) Prepare(ctx context.Context, target domain.MachineRef, actor, generation string) (WorkerIdentity, error) {
	if target == "" || actor == "" || generation == "" || c.opener == nil {
		return WorkerIdentity{}, ErrRequest
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	key := poolKey{target, actor, generation}
	w, release, err := c.acquire(ctx, key, true)
	if err != nil {
		return WorkerIdentity{}, err
	}
	defer release()

	if w.channel != nil {
		return w.identity, nil
	}
	ch, identity, err := c.openWorker(ctx, target)
	if err != nil {
		c.evict(key, w)
		return WorkerIdentity{}, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.workers[key] != w {
		_ = ch.Close()
		return WorkerIdentity{}, ErrClosed
	}
	w.channel = ch
	w.identity = identity
	return identity, nil
}

func (c *Client) openWorker(ctx context.Context, target domain.MachineRef) (guestssh.Duplex, WorkerIdentity, error) {
	started := time.Now()
	ch, err := c.opener.OpenDuplex(ctx, target, StartupCommand)
	failure := StartupFailure{Stage: "open", OpenMillis: time.Since(started).Milliseconds()}
	if err != nil {
		return nil, WorkerIdentity{}, startupFailure(ctx, nil, failure, err)
	}
	stop := context.AfterFunc(ctx, func() { _ = ch.Close() })
	defer stop()
	var identity WorkerIdentity
	failure.Stage = "source_write"
	started = time.Now()
	err = writeSource(ch)
	failure.SourceWriteMillis = time.Since(started).Milliseconds()
	if err == nil {
		failure.Stage = "hello"
		started = time.Now()
		err = readControl(ch, &identity)
		failure.HelloMillis = time.Since(started).Milliseconds()
	}
	if err == nil && (identity.PID <= 0 || identity.BirthTicks <= 0 || identity.Image == "" || identity.SID == "") {
		err = ErrProtocol
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		err = startupFailure(ctx, ch, failure, err)
		_ = ch.Close()
		return nil, WorkerIdentity{}, err
	}
	return ch, identity, nil
}
