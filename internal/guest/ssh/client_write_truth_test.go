package ssh

import (
	"context"
	"errors"
	"testing"
)

type cancelingWriter struct{ cancel context.CancelFunc }

func (w cancelingWriter) Write(p []byte) (int, error) {
	w.cancel()
	return len(p), nil
}

func (cancelingWriter) Close() error { return nil }

func TestSSHChannelWritePreservesAcceptedBytesWhenContextCancelsBeforeReturn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	channel := &sshChannel{stdin: cancelingWriter{cancel: cancel}, writeLane: make(chan struct{}, 1), closeLane: make(chan struct{}, 1)}
	n, err := channel.Write(ctx, []byte("synthetic input"))
	if n != len("synthetic input") || !errors.Is(err, context.Canceled) {
		t.Fatalf("Write = %d, %v; want accepted bytes and context cancellation", n, err)
	}
}
