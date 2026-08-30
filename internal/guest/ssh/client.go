package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	gossh "golang.org/x/crypto/ssh"
)

// Transport abstracts SSH network connection and pseudo-terminal allocation.
type Transport interface {
	Dial(ctx context.Context, target domain.MachineRef, cols, rows uint16, term string) (Channel, error)
}

// Channel represents an active bi-directional SSH pseudo-terminal session.
type Channel interface {
	io.Reader
	Write(ctx context.Context, p []byte) (int, error)
	SendControl(ctx context.Context, key domain.ControlKey) (ControlResult, error)
	Resize(cols, rows uint16) error
	Close(ctx context.Context) error
	Wait() (exitCode int, err error)
}

// ControlResult reports transport-owned truth for an attempted control effect.
type ControlResult struct {
	AcceptedBytes int
	EffectApplied bool
}

// CloseOutcome reports whether transport cleanup completed and the immutable result of that attempt.
type CloseOutcome struct {
	Complete bool
	Err      error
}

// CloseOutcomeProvider is implemented by channels that can distinguish a retryable no-effect close from completed cleanup.
type CloseOutcomeProvider interface {
	LastCloseOutcome() CloseOutcome
}

// NativeTransport dials target machines using SSH protocol and allocates pseudo-terminals.
type NativeTransport struct {
	keyProvider KeyProvider
}

// NewTransport creates a NativeTransport configured with the given KeyProvider.
func NewTransport(kp KeyProvider) *NativeTransport {
	return &NativeTransport{keyProvider: kp}
}

type dialConfig struct {
	endpoint string
	config   *gossh.ClientConfig
	cols     uint16
	rows     uint16
	term     string
}

func (t *NativeTransport) resolveDialConfig(ctx context.Context, target domain.MachineRef, cols, rows uint16, term string) (*dialConfig, error) {
	if t.keyProvider == nil {
		return nil, errors.New("ssh: key provider is nil")
	}

	signer, err := t.keyProvider.GetClientSigner(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("ssh: client signer resolution failed: %w", err)
	}

	hostKeyCallback, err := t.keyProvider.GetHostKeyCallback(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("ssh: host key callback resolution failed: %w", err)
	}

	guestUser, err := t.keyProvider.GetGuestUser(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("ssh: guest user resolution failed: %w", err)
	}

	endpoint, err := t.keyProvider.GetGuestEndpoint(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("ssh: guest endpoint resolution failed: %w", err)
	}

	if cols == 0 {
		cols = domain.DefaultCols
	}
	if rows == 0 {
		rows = domain.DefaultRows
	}
	if term == "" {
		term = domain.DefaultTermType
	}
	if err := domain.ValidateTerminalDimensions(cols, rows); err != nil {
		return nil, err
	}
	if err := domain.ValidateTerminalType(term); err != nil {
		return nil, err
	}

	config := &gossh.ClientConfig{
		User:            guestUser,
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: hostKeyCallback,
		Timeout:         10 * time.Second,
	}

	return &dialConfig{
		endpoint: endpoint,
		config:   config,
		cols:     cols,
		rows:     rows,
		term:     term,
	}, nil
}

// Dial establishes an SSH connection, allocates a PTY, and launches a remote shell.
func (t *NativeTransport) Dial(ctx context.Context, target domain.MachineRef, cols, rows uint16, term string) (Channel, error) {
	dc, err := t.resolveDialConfig(ctx, target, cols, rows, term)
	if err != nil {
		return nil, err
	}

	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", dc.endpoint)
	if err != nil {
		return nil, fmt.Errorf("ssh: TCP connection failed: %w", err)
	}
	stopCancellation := context.AfterFunc(ctx, func() {
		_ = conn.SetDeadline(time.Now())
	})
	defer stopCancellation()
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("ssh: failed to apply connection deadline: %w", err)
		}
	}

	sshConn, chans, reqs, err := gossh.NewClientConn(conn, dc.endpoint, dc.config)
	if err != nil {
		_ = conn.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("ssh: SSH handshake failed: %w", err)
	}

	client := gossh.NewClient(sshConn, chans, reqs)
	channel, err := openSessionPTY(conn, client, dc.cols, dc.rows, dc.term)
	if err != nil {
		_ = client.Close()
		_ = conn.Close()
		return nil, err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = channel.Close(context.Background())
		return nil, fmt.Errorf("ssh: failed to clear connection deadline: %w", err)
	}

	return channel, nil
}

func openSessionPTY(conn net.Conn, client *gossh.Client, cols, rows uint16, term string) (Channel, error) {
	session, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("ssh: failed to open session channel: %w", err)
	}

	modes := gossh.TerminalModes{
		gossh.ECHO:          1,
		gossh.TTY_OP_ISPEED: 115200,
		gossh.TTY_OP_OSPEED: 115200,
	}

	if err := session.RequestPty(term, int(rows), int(cols), modes); err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("ssh: pty-req failed: %w", err)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("ssh: stdin pipe failed: %w", err)
	}

	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("ssh: stdout pipe failed: %w", err)
	}

	stderr, err := session.StderrPipe()
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("ssh: stderr pipe failed: %w", err)
	}

	if err := session.Shell(); err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("ssh: shell request failed: %w", err)
	}

	pr, pw := io.Pipe()
	go func() {
		var wg sync.WaitGroup
		wg.Add(2)
		copyStream := func(r io.Reader) {
			defer wg.Done()
			buf := make([]byte, 4096)
			for {
				n, rErr := r.Read(buf)
				if n > 0 {
					_, _ = pw.Write(buf[:n])
				}
				if rErr != nil {
					break
				}
			}
		}
		go copyStream(stdout)
		go copyStream(stderr)
		wg.Wait()
		_ = pw.Close()
	}()

	return &sshChannel{
		conn:      conn,
		client:    client,
		session:   session,
		stdin:     stdin,
		reader:    pr,
		writeLane: make(chan struct{}, 1),
		closeLane: make(chan struct{}, 1),
	}, nil
}

type sshChannel struct {
	conn    net.Conn
	client  *gossh.Client
	session *gossh.Session
	stdin   io.WriteCloser
	reader  io.Reader

	mu            sync.Mutex
	writeLane     chan struct{}
	closeLane     chan struct{}
	closed        bool
	closeComplete bool
	closeErr      error
	waitOnce      sync.Once
	exitCode      int
	waitErr       error
}

func (c *sshChannel) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func (c *sshChannel) Write(ctx context.Context, p []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, domain.ErrSessionClosed
	}
	c.mu.Unlock()

	if err := acquireContextLane(ctx, c.writeLane); err != nil {
		return 0, err
	}
	defer releaseContextLane(c.writeLane)

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, domain.ErrSessionClosed
	}
	c.mu.Unlock()

	stopCancellation := func() bool { return false }
	if c.conn != nil {
		stopCancellation = context.AfterFunc(ctx, func() {
			_ = c.conn.SetWriteDeadline(time.Now())
		})
		if dl, ok := ctx.Deadline(); ok {
			_ = c.conn.SetWriteDeadline(dl)
		}
		defer func() {
			stopCancellation()
			_ = c.conn.SetWriteDeadline(time.Time{})
		}()
	}

	n, err := c.stdin.Write(p)
	if ctxErr := ctx.Err(); ctxErr != nil {
		if n > 0 {
			return n, errors.Join(err, ctxErr)
		}
		return 0, errors.Join(err, ctxErr)
	}
	return n, err
}

func (c *sshChannel) SendControl(ctx context.Context, key domain.ControlKey) (ControlResult, error) {
	norm, err := domain.NormalizeControlKey(string(key))
	if err != nil {
		return ControlResult{}, err
	}
	bytes := norm.ToBytes()
	if len(bytes) == 0 {
		return ControlResult{}, domain.ErrInvalidControlKey
	}
	n, writeErr := c.Write(ctx, bytes)
	return ControlResult{AcceptedBytes: n, EffectApplied: n > 0}, writeErr
}

func (c *sshChannel) Resize(cols, rows uint16) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return domain.ErrSessionClosed
	}
	return c.session.WindowChange(int(rows), int(cols))
}

func (c *sshChannel) Close(ctx context.Context) error {
	if err := acquireContextLane(ctx, c.closeLane); err != nil {
		return err
	}
	defer releaseContextLane(c.closeLane)

	if complete, err := c.closeResult(); complete {
		return err
	}
	if err := acquireContextLane(ctx, c.writeLane); err != nil {
		return err
	}
	defer releaseContextLane(c.writeLane)

	cancelDeadline := c.bindCloseDeadline(ctx)
	defer cancelDeadline()
	var errs []error
	if err := c.closeInputLocked(); err != nil {
		errs = append(errs, err)
	}
	errs = append(errs, c.closeTransportResources()...)
	if ctxErr := ctx.Err(); ctxErr != nil {
		errs = append(errs, ctxErr)
	}
	closeErr := errors.Join(errs...)
	c.finishClose(closeErr)
	return closeErr
}

func (c *sshChannel) closeResult() (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closeComplete {
		return true, c.closeErr
	}
	c.closed = true
	return false, nil
}

func (c *sshChannel) LastCloseOutcome() CloseOutcome {
	c.mu.Lock()
	defer c.mu.Unlock()
	return CloseOutcome{Complete: c.closeComplete, Err: c.closeErr}
}

func (c *sshChannel) finishClose(err error) {
	c.mu.Lock()
	c.closeComplete = true
	c.closeErr = err
	c.mu.Unlock()
}

func (c *sshChannel) closeInputLocked() error {
	if c.stdin != nil {
		return c.stdin.Close()
	}
	return nil
}

func acquireContextLane(ctx context.Context, lane chan struct{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case lane <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-lane
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func releaseContextLane(lane chan struct{}) {
	<-lane
}

func (c *sshChannel) bindCloseDeadline(ctx context.Context) func() {
	if c.conn == nil {
		return func() {}
	}
	stopCancellation := context.AfterFunc(ctx, func() {
		_ = c.conn.SetDeadline(time.Now())
	})
	if dl, ok := ctx.Deadline(); ok {
		_ = c.conn.SetDeadline(dl)
	} else {
		_ = c.conn.SetDeadline(time.Time{})
	}
	return func() { stopCancellation() }
}

func (c *sshChannel) closeTransportResources() []error {
	var errs []error
	if c.session != nil {
		if err := c.session.Close(); err != nil && !errors.Is(err, io.EOF) {
			errs = append(errs, err)
		}
	}
	if c.client != nil {
		if err := c.client.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if c.conn != nil {
		if err := c.conn.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			errs = append(errs, err)
		}
	}
	return errs
}

func (c *sshChannel) Wait() (int, error) {
	c.waitOnce.Do(func() {
		err := c.session.Wait()
		if err == nil {
			c.exitCode = 0
			c.waitErr = nil
			return
		}
		var exitErr *gossh.ExitError
		if errors.As(err, &exitErr) {
			c.exitCode = exitErr.ExitStatus()
			c.waitErr = nil
			return
		}
		var exitMissingErr *gossh.ExitMissingError
		if errors.As(err, &exitMissingErr) {
			c.exitCode = 0
			c.waitErr = nil
			return
		}
		c.exitCode = -1
		c.waitErr = err
	})
	return c.exitCode, c.waitErr
}
