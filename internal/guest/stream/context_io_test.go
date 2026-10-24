package stream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestOwnedIOCancelsBlockedPipeReadAndWrite(t *testing.T) {
	for _, operation := range []string{"read", "write"} {
		t.Run(operation, func(t *testing.T) {
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				close(started)
				var err error
				if operation == "read" {
					_, err = OwnedInput(reader).ReadContext(ctx, make([]byte, 1))
				} else {
					_, err = OwnedOutput(writer).WriteContext(ctx, []byte{1})
				}
				done <- err
			}()
			<-started
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("owned IO failed to unblock")
			}
		})
	}
}
func TestContextIORejectsUnsupportedBeforeDispatch(t *testing.T) {
	f := &fixtureOpener{}
	c := fixtureClient(t, f)
	_, err := c.Prepare(context.Background(), "target", "actor", "g")
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []io.Reader{bytes.NewReader(nil), io.NopCloser(bytes.NewReader(nil))} {
		if _, err = c.Execute(context.Background(), "target", "actor", "g", request(), input, nil); !errors.Is(err, ErrRequest) {
			t.Fatal("unsupported input admitted", err)
		}
	}
	if _, err = c.Execute(context.Background(), "target", "actor", "g", request(), nil, &bytes.Buffer{}); !errors.Is(err, ErrRequest) {
		t.Fatal("unsupported output admitted", err)
	}
	if f.requests.Load() != 0 {
		t.Fatal("unsupported IO wrote request")
	}
	if _, err = c.Execute(context.Background(), "target", "actor", "g", request(), BytesInput(nil), BufferOutput(&bytes.Buffer{})); err != nil {
		t.Fatal(err)
	}
}
func TestCancelledMemoryIOHasNoEffects(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var buffer bytes.Buffer
	if _, err := BytesInput([]byte{1}).ReadContext(ctx, make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := BufferOutput(&buffer).WriteContext(ctx, []byte{1}); !errors.Is(err, context.Canceled) || buffer.Len() != 0 {
		t.Fatal(err)
	}
}

type signalledReader struct {
	*io.PipeReader
	started chan struct{}
}

func (r signalledReader) Read(p []byte) (int, error) { close(r.started); return r.PipeReader.Read(p) }

type signalledWriter struct {
	*io.PipeWriter
	started chan struct{}
}

func (w signalledWriter) Write(p []byte) (int, error) { close(w.started); return w.PipeWriter.Write(p) }
func TestExecuteCancellationUnblocksCallerIO(t *testing.T) {
	for _, operation := range []string{"upload", "download"} {
		t.Run(operation, func(t *testing.T) { testExecuteCancellationIO(t, operation) })
	}
}
func testExecuteCancellationIO(t *testing.T, operation string) {
	t.Helper()
	started := make(chan struct{})
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	f := &fixtureOpener{}
	req := request()
	var input io.Reader
	var output io.Writer
	if operation == "upload" {
		req.Size = 1
		req.SHA256 = hash([]byte{1})
		input = OwnedInput(signalledReader{reader, started})
	} else {
		req.Operation = "get"
		req.Path = "synthetic"
		req.Owned = []FileIdentity{{Path: req.Path}}
		req.MaxOutput = 1
		output = OwnedOutput(signalledWriter{writer, started})
		f.handler = func(ch net.Conn, request Request) {
			result := Result{ID: request.ID, Size: 1, SHA256: hash([]byte{1}), Worker: WorkerIdentity{PID: 12, BirthTicks: 123, Image: "synthetic-worker", SID: "synthetic-sid"}}
			_ = writeControl(ch, result)
			_, _ = ch.Write([]byte{1})
			_ = writeControl(ch, result)
		}
	}
	c := fixtureClient(t, f)
	if _, err := c.Prepare(context.Background(), "target", "actor", "g"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.Execute(ctx, "target", "actor", "g", req, input, output); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("caller IO never entered")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Execute remained blocked in caller IO")
	}
	f.wg.Wait()
	if f.count.Load() != 1 || f.requests.Load() != 1 {
		t.Fatal("cancelled request replayed")
	}
}

func TestHTTPInputCancelsExactRequestBeforeClosingBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	requestContext, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	done := make(chan error, 1)
	body := signalBody{response.Body, started}
	go func() { _, err := HTTPInput(body, cancelRequest).ReadContext(ctx, make([]byte, 1)); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP body remained blocked")
	}
	if !errors.Is(requestContext.Err(), context.Canceled) {
		t.Fatal("exact HTTP request remained active")
	}
}

type signalBody struct {
	io.ReadCloser
	started chan struct{}
}

func (b signalBody) Read(p []byte) (int, error) { close(b.started); return b.ReadCloser.Read(p) }

func TestMemoryWrappersPreserveBytes(t *testing.T) {
	payload := []byte{0, 255, 128}
	got := make([]byte, 3)
	if n, err := BytesInput(payload).ReadContext(context.Background(), got); err != nil || n != 3 || !bytes.Equal(got, payload) {
		t.Fatal(got, n, err)
	}
	var buffer bytes.Buffer
	if n, err := BufferOutput(&buffer).WriteContext(context.Background(), payload); err != nil || n != 3 || !bytes.Equal(buffer.Bytes(), payload) {
		t.Fatal(n, err)
	}
}

func TestOwnedIOCompletesWithoutCancellation(t *testing.T) {
	path := t.TempDir() + "/synthetic"
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	payload := []byte{1, 2, 0, 255}
	if n, err := OwnedOutput(file).WriteContext(context.Background(), payload); err != nil || n != len(payload) {
		t.Fatal(n, err)
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, len(payload))
	if n, err := OwnedInput(file).ReadContext(context.Background(), buffer); err != nil || n != len(payload) || !bytes.Equal(buffer, payload) {
		t.Fatal(n, err)
	}
	if OwnedInput(nil) != nil || OwnedOutput(nil) != nil || BufferOutput(nil) != nil || HTTPInput(file, nil) != nil {
		t.Fatal("nil IO wrapper accepted")
	}
}
