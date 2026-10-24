package stream

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

type poolKey struct {
	target     domain.MachineRef
	actor      string
	generation string
}
type worker struct {
	lane     chan struct{}
	channel  guestssh.Duplex
	identity WorkerIdentity
}
type Client struct {
	opener  DuplexOpener
	mu      sync.Mutex
	workers map[poolKey]*worker
	closed  bool
}

func NewClient(opener DuplexOpener) *Client {
	return &Client{opener: opener, workers: make(map[poolKey]*worker)}
}
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	var errs []error
	for k, w := range c.workers {
		if w.channel != nil {
			errs = append(errs, w.channel.Close())
		}
		delete(c.workers, k)
	}
	return errors.Join(errs...)
}
func validDigest(s string) bool {
	p, e := hex.DecodeString(s)
	return e == nil && len(p) == 32 && s == strings.ToLower(s)
}

// Execute sends exactly once. A broken or cancelled worker is evicted; this
// operation is never replayed, even when the remote effect is uncertain.
func (c *Client) Execute(ctx context.Context, target domain.MachineRef, actor string, generation string, req Request, input io.Reader, output io.Writer) (Result, error) {
	if err := validate(req); err != nil {
		return Result{}, err
	}
	if actor == "" || target == "" || c.opener == nil {
		return Result{}, ErrRequest
	}
	ctx, cancel := context.WithDeadline(ctx, req.DeadlineUTC)
	defer cancel()
	input, output, err := bindRequestIO(ctx, input, output)
	if err != nil {
		return Result{}, err
	}
	key := poolKey{target, actor, generation}
	w, release, err := c.acquire(ctx, key, false)
	if err != nil {
		return Result{}, err
	}
	defer release()

	if w.channel == nil {
		return Result{}, ErrRequest
	}
	stop := context.AfterFunc(ctx, func() { _ = w.channel.Close() })
	defer stop()
	result, err := exchange(w.channel, req, input, output, w.identity)
	if err != nil {
		err = observeLifetimeExit(ctx, w.channel, err)
		c.evict(key, w)
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
	}
	return result, err
}
func (c *Client) evict(key poolKey, w *worker) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if w.channel != nil {
		_ = w.channel.Close()
	}
	if c.workers[key] == w {
		delete(c.workers, key)
	}
}
func exchange(ch io.ReadWriter, req Request, input io.Reader, output io.Writer, identity WorkerIdentity) (Result, error) {
	if err := writeControl(ch, req); err != nil {
		return Result{}, err
	}
	if req.Operation == "put" || req.Operation == "exec" {
		if err := sendPayload(ch, req, input); err != nil {
			return Result{}, err
		}
	}
	return receiveResult(ch, req, output, identity)
}
func receiveResult(ch io.Reader, req Request, output io.Writer, identity WorkerIdentity) (Result, error) {
	var header Result
	if err := readControl(ch, &header); err != nil {
		return Result{}, err
	}
	if header.ControlFailure != nil {
		return receiveControlFailure(ch, header, identity)
	}
	return receiveOperationResult(ch, req, output, identity, header)
}

func receiveOperationResult(ch io.Reader, req Request, output io.Writer, identity WorkerIdentity, header Result) (Result, error) {
	if header.ID != req.ID || header.Size < 0 || header.Size > req.MaxOutput || header.Worker != identity {
		return Result{}, ErrProtocol
	}
	if invalidFailureHeader(header) {
		return Result{}, ErrProtocol
	}
	if output == nil {
		output = io.Discard
	}
	digest, err := copyPayload(output, ch, header.Size)
	if err != nil {
		return header, err
	}
	var result Result
	if err = readControl(ch, &result); err != nil {
		return header, err
	}
	if result.ID != req.ID || result.Worker != identity || result.Size != header.Size || !validDigest(result.SHA256) || digest != result.SHA256 {
		return result, ErrIntegrity
	}
	return result, verifyWorkerFailure(header, result)
}

func sendPayload(ch io.Writer, req Request, input io.Reader) error {
	if input == nil {
		input = strings.NewReader("")
	}
	digest, err := copyPayload(ch, input, req.Size)
	if err != nil {
		return err
	}
	var extra [1]byte
	n, readErr := input.Read(extra[:])
	if n != 0 || readErr != io.EOF || digest != req.SHA256 {
		return ErrIntegrity
	}
	if err = writeAll(ch, []byte{1}); err != nil {
		return err
	}
	return nil
}
