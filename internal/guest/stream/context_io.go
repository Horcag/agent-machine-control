package stream

import (
	"bytes"
	"context"
	"io"
)

// ContextReader must return promptly when ctx is cancelled, including when its
// underlying source blocks. Execute rejects raw readers before dispatch.
type ContextReader interface {
	io.Reader
	ReadContext(context.Context, []byte) (int, error)
}

// ContextWriter obeys the same cancellation contract for blocked sinks.
type ContextWriter interface {
	io.Writer
	WriteContext(context.Context, []byte) (int, error)
}

type bytesInput struct{ *bytes.Reader }

func (b bytesInput) ReadContext(ctx context.Context, p []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return b.Read(p)
}

// BytesInput wraps nonblocking immutable bytes; the caller keeps them immutable
// until Execute returns.
func BytesInput(data []byte) ContextReader { return bytesInput{bytes.NewReader(data)} }

type bufferOutput struct{ *bytes.Buffer }

func (b bufferOutput) WriteContext(ctx context.Context, p []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return b.Write(p)
}
func BufferOutput(buffer *bytes.Buffer) ContextWriter {
	if buffer == nil {
		return nil
	}
	return bufferOutput{buffer}
}

type ownedInput struct{ io.ReadCloser }

// OwnedInput grants cancellation authority over this exact source. Close must
// promptly unblock Read (files, pipes and network bodies); wrapping an arbitrary
// blocking reader with io.NopCloser does not satisfy this contract. The caller
// still closes the source after successful completion.
func OwnedInput(source io.ReadCloser) ContextReader {
	if source == nil {
		return nil
	}
	return ownedInput{source}
}
func (r ownedInput) ReadContext(ctx context.Context, p []byte) (int, error) {
	stop, err := bindOwnedIO(ctx, r.ReadCloser)
	if err != nil {
		return 0, err
	}
	defer stop()
	n, err := r.Read(p)
	if ctx.Err() != nil {
		return n, ctx.Err()
	}
	return n, err
}

type ownedOutput struct{ io.WriteCloser }

// OwnedOutput grants the same cancellation authority over an owned sink whose
// Close promptly unblocks Write. The caller closes it on normal completion.
func OwnedOutput(sink io.WriteCloser) ContextWriter {
	if sink == nil {
		return nil
	}
	return ownedOutput{sink}
}
func (w ownedOutput) WriteContext(ctx context.Context, p []byte) (int, error) {
	stop, err := bindOwnedIO(ctx, w.WriteCloser)
	if err != nil {
		return 0, err
	}
	defer stop()
	n, err := w.Write(p)
	if ctx.Err() != nil {
		return n, ctx.Err()
	}
	return n, err
}
func bindOwnedIO(ctx context.Context, closer io.Closer) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	cancel := context.AfterFunc(ctx, func() { defer close(done); _ = closer.Close() })
	return func() {
		if !cancel() {
			<-done
		}
	}, nil
}

type requestReader struct {
	ctx    context.Context
	source ContextReader
}

func (r requestReader) Read(p []byte) (int, error) { return r.source.ReadContext(r.ctx, p) }

type requestWriter struct {
	ctx  context.Context
	sink ContextWriter
}

func (w requestWriter) Write(p []byte) (int, error) { return w.sink.WriteContext(w.ctx, p) }
func bindRequestIO(ctx context.Context, input io.Reader, output io.Writer) (io.Reader, io.Writer, error) {
	if input != nil {
		source, ok := input.(ContextReader)
		if !ok {
			return nil, nil, ErrRequest
		}
		input = requestReader{ctx, source}
	}
	if output != nil {
		sink, ok := output.(ContextWriter)
		if !ok {
			return nil, nil, ErrRequest
		}
		output = requestWriter{ctx, sink}
	}
	return input, output, nil
}

// HTTPInput owns cancellation of the exact outbound HTTP request and its body.
// Cancelling the request first wakes transport reads; Body.Close alone can wait
// on an in-flight Read. The caller closes the body after normal completion.
func HTTPInput(body io.ReadCloser, cancelRequest context.CancelFunc) ContextReader {
	if body == nil || cancelRequest == nil {
		return nil
	}
	return OwnedInput(httpInput{body, cancelRequest})
}

type httpInput struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (body httpInput) Close() error { body.cancel(); return body.ReadCloser.Close() }
