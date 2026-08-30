package ssh

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestSSHChannelCloseDeadlineDoesNotWaitForWriteLaneOrCloseLate(t *testing.T) {
	stdin := newBlockingWriteCloser()
	conn := &closeTrackingConn{}
	stdin.beforeClose = func() {
		if conn.deadlineCount.Load() == 0 {
			stdin.closeWithoutDeadline.Store(true)
		}
	}
	channel := newCloseTestChannel(stdin, conn)

	writeDone := make(chan error, 1)
	go func() {
		_, err := channel.Write(context.Background(), []byte("command"))
		writeDone <- err
	}()
	awaitSignal(t, stdin.writeStarted, "write to acquire lane")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	closeDone := make(chan error, 1)
	go func() { closeDone <- channel.Close(ctx) }()

	select {
	case err := <-closeDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Close error = %v, want context deadline exceeded", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Close waited for the occupied write lane past its deadline")
	}
	assertNoCloseEffects(t, stdin, conn, "after timed-out Close")

	close(stdin.releaseWrite)
	if err := awaitError(t, writeDone, "blocked write"); err != nil {
		t.Fatalf("Write error = %v, want nil", err)
	}
	select {
	case <-stdin.closeCalled:
		t.Fatal("timed-out Close performed a late stdin close")
	case <-time.After(30 * time.Millisecond):
	}
	assertNoCloseEffects(t, stdin, conn, "after blocked write released")

	assertCloseSucceedsOnce(t, channel, stdin, conn)
	if err := channel.Close(context.Background()); err != nil {
		t.Fatalf("idempotent Close failed: %v", err)
	}
	if got := stdin.closeCount.Load(); got != 1 {
		t.Fatalf("stdin close count after idempotent Close = %d, want 1", got)
	}
}

func TestSSHChannelConcurrentWriteControlAndCloseRemainSerialized(t *testing.T) {
	stdin := newBlockingWriteCloser()
	channel := newCloseTestChannel(stdin, &closeTrackingConn{})

	firstWriteDone := make(chan error, 1)
	go func() {
		_, err := channel.Write(context.Background(), []byte("command"))
		firstWriteDone <- err
	}()
	awaitSignal(t, stdin.writeStarted, "first write to acquire lane")

	controlStarted := make(chan struct{})
	controlDone := make(chan error, 1)
	go func() {
		close(controlStarted)
		_, err := channel.SendControl(context.Background(), domain.ControlKeyCtrlC)
		controlDone <- err
	}()
	awaitSignal(t, controlStarted, "control call to start")

	closeStarted := make(chan struct{})
	closeDone := make(chan error, 1)
	go func() {
		close(closeStarted)
		closeDone <- channel.Close(context.Background())
	}()
	awaitSignal(t, closeStarted, "close call to start")

	select {
	case <-stdin.closeCalled:
		t.Fatal("Close raced with the active write")
	case <-time.After(30 * time.Millisecond):
	}
	close(stdin.releaseWrite)

	if err := awaitError(t, firstWriteDone, "first write"); err != nil {
		t.Fatalf("first Write error = %v, want nil", err)
	}
	controlErr := awaitError(t, controlDone, "control call")
	if controlErr != nil && !errors.Is(controlErr, domain.ErrSessionClosed) {
		t.Fatalf("SendControl error = %v, want nil or session closed", controlErr)
	}
	if err := awaitError(t, closeDone, "close call"); err != nil {
		t.Fatalf("Close error = %v, want nil", err)
	}
	if stdin.overlap.Load() {
		t.Fatal("write, control, or stdin close overlapped")
	}
	if got := stdin.closeCount.Load(); got != 1 {
		t.Fatalf("stdin close count = %d, want 1", got)
	}
	if err := channel.Close(context.Background()); err != nil {
		t.Fatalf("idempotent Close failed: %v", err)
	}
	if got := stdin.closeCount.Load(); got != 1 {
		t.Fatalf("stdin close count after repeated Close = %d, want 1", got)
	}
}

func TestSSHChannelWriteAndControlLaneAcquisitionHonorContext(t *testing.T) {
	stdin := newBlockingWriteCloser()
	channel := newCloseTestChannel(stdin, &closeTrackingConn{})

	firstWriteDone := make(chan error, 1)
	go func() {
		_, err := channel.Write(context.Background(), []byte("command"))
		firstWriteDone <- err
	}()
	awaitSignal(t, stdin.writeStarted, "first write to acquire lane")

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelWrite()
	if _, err := channel.Write(writeCtx, []byte("late write")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting Write error = %v, want context deadline exceeded", err)
	}

	controlCtx, cancelControl := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelControl()
	if _, err := channel.SendControl(controlCtx, domain.ControlKeyCtrlC); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting SendControl error = %v, want context deadline exceeded", err)
	}

	close(stdin.releaseWrite)
	if err := awaitError(t, firstWriteDone, "first write"); err != nil {
		t.Fatalf("first Write error = %v, want nil", err)
	}
	time.Sleep(30 * time.Millisecond)
	if got := stdin.writeCount.Load(); got != 1 {
		t.Fatalf("stdin write count = %d, want 1 with no late write or control", got)
	}
}

func TestSSHChannelSendControlPreservesAcceptedBytesWhenContextCancelsAfterWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stdin := &cancelAfterWriteCloser{cancel: cancel}
	channel := &sshChannel{
		stdin:     stdin,
		writeLane: make(chan struct{}, 1),
		closeLane: make(chan struct{}, 1),
	}

	result, err := channel.SendControl(ctx, domain.ControlKeyCtrlC)
	if result.AcceptedBytes != 1 || !result.EffectApplied || !errors.Is(err, context.Canceled) {
		t.Fatalf("SendControl() = (%+v, %v), want one accepted byte, applied effect, and context canceled", result, err)
	}
}

func TestSSHChannelCloseReportsTransportFailuresTruthfully(t *testing.T) {
	stdinErr := errors.New("stdin close failed")
	connErr := errors.New("connection close failed")
	stdin := newBlockingWriteCloser()
	close(stdin.releaseWrite)
	stdin.closeErr = stdinErr
	conn := &closeTrackingConn{closeErr: connErr}
	channel := newCloseTestChannel(stdin, conn)

	err := channel.Close(context.Background())
	if !errors.Is(err, stdinErr) || !errors.Is(err, connErr) {
		t.Fatalf("Close error = %v, want joined stdin and connection failures", err)
	}
	if err := channel.Close(context.Background()); !errors.Is(err, stdinErr) || !errors.Is(err, connErr) {
		t.Fatalf("idempotent Close after reported failure = %v, want cached joined failure", err)
	}
	if got := stdin.closeCount.Load(); got != 1 {
		t.Fatalf("stdin close count = %d, want 1", got)
	}
	if got := conn.closeCount.Load(); got != 1 {
		t.Fatalf("connection close count = %d, want 1", got)
	}
}

func newCloseTestChannel(stdin io.WriteCloser, conn net.Conn) *sshChannel {
	return &sshChannel{
		conn:      conn,
		stdin:     stdin,
		writeLane: make(chan struct{}, 1),
		closeLane: make(chan struct{}, 1),
	}
}

func awaitSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func awaitError(t *testing.T, result <-chan error, name string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for %s", name)
		return nil
	}
}

func assertNoCloseEffects(t *testing.T, stdin *blockingWriteCloser, conn *closeTrackingConn, when string) {
	t.Helper()
	if got := stdin.closeCount.Load(); got != 0 {
		t.Fatalf("stdin close count %s = %d, want 0", when, got)
	}
	if got := conn.closeCount.Load(); got != 0 {
		t.Fatalf("connection close count %s = %d, want 0", when, got)
	}
	if got := conn.deadlineCount.Load(); got != 0 {
		t.Fatalf("connection deadline count %s = %d, want 0", when, got)
	}
}

func assertCloseSucceedsOnce(t *testing.T, channel *sshChannel, stdin *blockingWriteCloser, conn *closeTrackingConn) {
	t.Helper()
	if err := channel.Close(context.Background()); err != nil {
		t.Fatalf("retry Close failed: %v", err)
	}
	if got := stdin.closeCount.Load(); got != 1 {
		t.Fatalf("stdin close count after retry = %d, want 1", got)
	}
	if got := conn.closeCount.Load(); got != 1 {
		t.Fatalf("connection close count after retry = %d, want 1", got)
	}
	if stdin.closeWithoutDeadline.Load() {
		t.Fatal("stdin closed before the connection close deadline was bound")
	}
}

type blockingWriteCloser struct {
	writeStarted chan struct{}
	releaseWrite chan struct{}
	closeCalled  chan struct{}
	writeOnce    sync.Once
	closeOnce    sync.Once
	active       atomic.Int32
	overlap      atomic.Bool
	writeCount   atomic.Int32
	closeCount   atomic.Int32
	closeErr     error
	beforeClose  func()

	closeWithoutDeadline atomic.Bool
}

type cancelAfterWriteCloser struct{ cancel context.CancelFunc }

func (w *cancelAfterWriteCloser) Write(p []byte) (int, error) {
	w.cancel()
	return len(p), nil
}

func (*cancelAfterWriteCloser) Close() error { return nil }

func newBlockingWriteCloser() *blockingWriteCloser {
	return &blockingWriteCloser{
		writeStarted: make(chan struct{}),
		releaseWrite: make(chan struct{}),
		closeCalled:  make(chan struct{}),
	}
}

func (w *blockingWriteCloser) Write(p []byte) (int, error) {
	if w.active.Add(1) != 1 {
		w.overlap.Store(true)
	}
	w.writeOnce.Do(func() { close(w.writeStarted) })
	<-w.releaseWrite
	w.writeCount.Add(1)
	w.active.Add(-1)
	return len(p), nil
}

func (w *blockingWriteCloser) Close() error {
	if w.active.Add(1) != 1 {
		w.overlap.Store(true)
	}
	if w.beforeClose != nil {
		w.beforeClose()
	}
	w.closeCount.Add(1)
	w.closeOnce.Do(func() { close(w.closeCalled) })
	w.active.Add(-1)
	return w.closeErr
}

type closeTrackingConn struct {
	closeCount    atomic.Int32
	deadlineCount atomic.Int32
	closeErr      error
}

func (c *closeTrackingConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *closeTrackingConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *closeTrackingConn) Close() error                     { c.closeCount.Add(1); return c.closeErr }
func (c *closeTrackingConn) LocalAddr() net.Addr              { return closeTestAddr("local") }
func (c *closeTrackingConn) RemoteAddr() net.Addr             { return closeTestAddr("remote") }
func (c *closeTrackingConn) SetDeadline(time.Time) error      { c.deadlineCount.Add(1); return nil }
func (c *closeTrackingConn) SetReadDeadline(time.Time) error  { return nil }
func (c *closeTrackingConn) SetWriteDeadline(time.Time) error { return nil }

type closeTestAddr string

func (a closeTestAddr) Network() string { return string(a) }
func (a closeTestAddr) String() string  { return string(a) }
