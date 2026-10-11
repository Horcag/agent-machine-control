package stream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

type fixtureChannel struct{ net.Conn }

func (f fixtureChannel) Wait() error { return nil }

type fixtureOpener struct {
	count    atomic.Int64
	requests atomic.Int64
	handler  func(net.Conn, Request)
	wg       sync.WaitGroup
}

func (f *fixtureOpener) OpenDuplex(_ context.Context, _ domain.MachineRef, _ string) (guestssh.Duplex, error) {
	f.count.Add(1)
	a, b := net.Pipe()
	f.wg.Go(func() {
		defer b.Close()
		var h [4]byte
		if _, err := io.ReadFull(b, h[:]); err != nil {
			return
		}
		n := int(h[0])<<24 | int(h[1])<<16 | int(h[2])<<8 | int(h[3])
		if _, err := io.CopyN(io.Discard, b, int64(n)); err != nil {
			return
		}
		_ = writeControl(b, WorkerIdentity{PID: 12, BirthTicks: 123, Image: "synthetic-worker", SID: "synthetic-sid"})
		f.serve(b)
	})
	return fixtureChannel{a}, nil
}
func fixtureClient(t *testing.T, f *fixtureOpener) *Client {
	t.Helper()
	c := NewClient(f)
	t.Cleanup(func() { _ = c.Close(); f.wg.Wait() })
	return c
}
func request() Request {
	return Request{ID: "synthetic-request", Operation: "put", DeadlineUTC: time.Now().Add(10 * time.Second), SHA256: hash(nil), ExpectedRoot: &RootIdentity{Path: "synthetic-root", FileID: "00112233445566778899aabbccddeeff", Owner: "synthetic-sid", SDDLHash: hash(nil)}}
}
func TestPrepareAndPoolIdentitySeparation(t *testing.T) {
	f := &fixtureOpener{}
	c := fixtureClient(t, f)
	ctx := context.Background()
	if _, err := c.Execute(ctx, "target", "actor", "generation", request(), nil, nil); !errors.Is(err, ErrRequest) {
		t.Fatal("unadmitted worker", err)
	}
	for _, key := range []poolKey{{"target", "actor", "g1"}, {"target", "actor2", "g1"}, {"target2", "actor", "g1"}, {"target", "actor", "g2"}} {
		for range 2 {
			identity, err := c.Prepare(ctx, key.target, key.actor, key.generation)
			if err != nil || identity.BirthTicks != 123 {
				t.Fatal(identity, err)
			}
			if _, err = c.Execute(ctx, key.target, key.actor, key.generation, request(), nil, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	if f.count.Load() != 4 || f.requests.Load() != 8 {
		t.Fatalf("pool: dials=%d requests=%d", f.count.Load(), f.requests.Load())
	}
}
func TestExecuteConcurrentSerialization(t *testing.T) {
	f := &fixtureOpener{}
	c := fixtureClient(t, f)
	_, err := c.Prepare(context.Background(), "target", "actor", "g")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for range 20 {
		wg.Go(func() {
			_, err := c.Execute(context.Background(), "target", "actor", "g", request(), nil, nil)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if f.count.Load() != 1 || f.requests.Load() != 20 {
		t.Fatal("worker was restarted")
	}
}
func TestFailureNeverReplays(t *testing.T) {
	f := &fixtureOpener{handler: func(ch net.Conn, _ Request) { _ = ch.Close() }}
	c := fixtureClient(t, f)
	_, _ = c.Prepare(context.Background(), "target", "actor", "g")
	if _, err := c.Execute(context.Background(), "target", "actor", "g", request(), nil, nil); err == nil {
		t.Fatal("missing failure")
	}
	if f.count.Load() != 1 || f.requests.Load() != 1 {
		t.Fatal("request replay")
	}
	if _, err := c.Execute(context.Background(), "target", "actor", "g", request(), nil, nil); !errors.Is(err, ErrRequest) {
		t.Fatal(err)
	}
	if f.count.Load() != 1 {
		t.Fatal("implicit restart")
	}
}
func TestCancellationClosesOnlyOwnedWorker(t *testing.T) {
	received := make(chan struct{})
	f := &fixtureOpener{handler: func(ch net.Conn, _ Request) { close(received); _, _ = io.Copy(io.Discard, ch) }}
	c := fixtureClient(t, f)
	_, _ = c.Prepare(context.Background(), "target", "actor", "g")
	_, _ = c.Prepare(context.Background(), "other", "actor", "g")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := c.Execute(ctx, "target", "actor", "g", request(), nil, nil); done <- err }()
	<-received
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not close channel")
	}
	if _, err := c.Prepare(context.Background(), "other", "actor", "g"); err != nil || f.count.Load() != 2 {
		t.Fatal("sibling evicted", err)
	}
}
func TestUploadRejectsSizeDigestAndExtraInput(t *testing.T) {
	for _, input := range []string{"short", "longer-data", "wrong"} {
		t.Run(input, func(t *testing.T) {
			f := &fixtureOpener{}
			c := fixtureClient(t, f)
			_, _ = c.Prepare(context.Background(), "target", "actor", "g")
			req := request()
			req.Size = 5
			req.SHA256 = hash([]byte("right"))
			_, err := c.Execute(context.Background(), "target", "actor", "g", req, bytes.NewBufferString(input), nil)
			if !errors.Is(err, ErrIntegrity) {
				t.Fatal(err)
			}
		})
	}
}

func (f *fixtureOpener) serve(b net.Conn) {
	for {
		var req Request
		if readControl(b, &req) != nil {
			return
		}
		f.requests.Add(1)
		if f.handler != nil {
			f.handler(b, req)
			return
		}
		if _, err := io.CopyN(io.Discard, b, req.Size); err != nil {
			return
		}
		if req.Operation == "put" || req.Operation == "exec" {
			var flag [1]byte
			if _, err := io.ReadFull(b, flag[:]); err != nil || flag[0] != 1 {
				return
			}
		}
		result := Result{ID: req.ID, Status: "ok", SHA256: hash(nil), Worker: WorkerIdentity{PID: 12, BirthTicks: 123, Image: "synthetic-worker", SID: "synthetic-sid"}}
		if writeControl(b, result) != nil {
			return
		}
		if writeControl(b, result) != nil {
			return
		}
	}
}

func TestRetireRequiresFreshPreparation(t *testing.T) {
	f := &fixtureOpener{}
	c := fixtureClient(t, f)
	_, err := c.Prepare(context.Background(), "target", "actor", "g")
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Retire("target", "actor", "g"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Execute(context.Background(), "target", "actor", "g", request(), nil, nil); !errors.Is(err, ErrRequest) {
		t.Fatal("retired admission reused", err)
	}
	if _, err = c.Prepare(context.Background(), "target", "actor", "g"); err != nil || f.count.Load() != 2 {
		t.Fatal("fresh worker absent", err)
	}
}

func BenchmarkWorkerReuse(b *testing.B) {
	f := &fixtureOpener{}
	c := NewClient(f)
	defer func() { _ = c.Close(); f.wg.Wait() }()
	_, err := c.Prepare(context.Background(), "target", "actor", "g")
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err = c.Execute(context.Background(), "target", "actor", "g", request(), nil, nil); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if f.count.Load() != 1 {
		b.Fatal("handshake repeated")
	}
	b.ReportMetric(float64(f.count.Load())/float64(b.N), "handshake/op")
}
