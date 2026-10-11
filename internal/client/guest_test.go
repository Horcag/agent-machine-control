package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
)

func guestFixture(t *testing.T, data []byte, mode string) (*Client, *int) {
	t.Helper()
	calls := new(int)
	sum := sha256.Sum256(data)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing authentication")
		}
		p, prefix, err := daemon.DecodeGuestMetadata(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		payload, err := io.ReadAll(r.Body)
		if err != nil || r.ContentLength != prefix+int64(len(payload)) || int64(len(payload)) != p.Size {
			t.Error("invalid request framing")
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Trailer", daemon.GuestResultTrailer)
		final := daemon.GuestFinal{Result: app.GuestOperationResult{Receipt: &domain.Receipt{ReceiptID: domain.ReceiptID("rcpt-" + strings.Repeat("a", 32)), Fingerprint: domain.Fingerprint("sha256:" + strings.Repeat("a", 64)), Outcome: domain.ExecutionOutcome{Status: domain.OutcomeSuccess}, OperationKind: domain.OperationKind("guest." + p.Operation), IdempotencyKey: p.IdempotencyKey, Target: "local:11111111-1111-4111-8111-111111111111", Actor: "operator:synthetic", Class: domain.ClassDestructivePrivileged, EffectiveBackend: "ssh", StartedAt: time.Now(), CompletedAt: time.Now(), ObservationType: domain.ObservationObserved, RedactionStatus: domain.RedactionNotApplicable}, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}}
		if mode == "hash" {
			final.Result.SHA256 = hex.EncodeToString(make([]byte, 32))
		}
		if mode == "size" {
			final.Result.Size++
		}
		if mode == "error" {
			final.Error = "uncertain"
		}
		for _, b := range data {
			_, _ = w.Write([]byte{b})
		}
		if mode != "missing" {
			encoded, _ := json.Marshal(final)
			w.Header().Set(daemon.GuestResultTrailer, base64.RawStdEncoding.EncodeToString(encoded))
		}
	}))
	t.Cleanup(server.Close)
	return New(server.URL, "test-token"), calls
}
func TestGuestBinaryTrailerIntegrity(t *testing.T) {
	for _, mode := range []string{"valid", "hash", "size", "error", "missing"} {
		t.Run(mode, func(t *testing.T) {
			data := []byte{0, 255, 1, 128}
			cl, calls := guestFixture(t, data, mode)
			var out bytes.Buffer
			result, err := cl.GuestOperation(context.Background(), app.GuestOperationParams{Operation: "get", IdempotencyKey: "synthetic-request", MaxOutput: 4, Deadline: time.Now().Add(time.Second)}, nil, stream.BufferOutput(&out))
			if (err == nil) != (mode == "valid") {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if *calls != 1 {
				t.Fatal("request was replayed")
			}
			if mode == "valid" && !bytes.Equal(data, out.Bytes()) {
				t.Fatal("binary changed")
			}
		})
	}
}
func TestGuestFilePublication(t *testing.T) {
	for _, mode := range []string{"valid", "hash", "missing"} {
		t.Run(mode, func(t *testing.T) {
			data := []byte("verified output")
			cl, _ := guestFixture(t, data, mode)
			directory := t.TempDir()
			path := filepath.Join(directory, "result.bin")
			_, err := cl.GuestOperationToFile(context.Background(), app.GuestOperationParams{Operation: "get", IdempotencyKey: "synthetic-request", MaxOutput: 100, Deadline: time.Now().Add(time.Second)}, nil, path)
			entries, _ := os.ReadDir(directory)
			if mode != "valid" {
				if err == nil || len(entries) != 0 {
					t.Fatalf("failed stream published or leaked: %v %v", err, entries)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, _ := os.ReadFile(path)
			if !bytes.Equal(got, data) {
				t.Fatal("published bytes differ")
			}
			if len(entries) != 1 {
				t.Fatal("staging file leaked")
			}

		})
	}
}
func TestGuestDestinationExistsDoesNotDispatch(t *testing.T) {
	cl, calls := guestFixture(t, []byte("x"), "valid")
	path := filepath.Join(t.TempDir(), "exists")
	_ = os.WriteFile(path, []byte("keep"), 0600)
	if _, err := cl.GuestOperationToFile(context.Background(), app.GuestOperationParams{}, nil, path); err == nil || *calls != 0 {
		t.Fatal("existing destination was dispatched or overwritten")
	}
}
func TestGuestSourceChangeRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source")
	_ = os.WriteFile(path, []byte("first"), 0600)
	source, err := OpenGuestSource(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	_ = os.WriteFile(path, []byte("changed"), 0600)
	if source.Verify() == nil {
		t.Fatal("source change accepted")
	}
}

func TestGuestCancellationUnblocksHTTPRead(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Trailer", daemon.GuestResultTrailer)
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		var out bytes.Buffer
		_, err := New(server.URL, "token").GuestOperation(ctx, app.GuestOperationParams{Operation: "get", IdempotencyKey: "synthetic-request", MaxOutput: 20, Deadline: time.Now().Add(5 * time.Second)}, nil, stream.BufferOutput(&out))
		done <- err
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancel accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("request cancellation did not unblock read")
	}
}

type shortGuestWriter struct{}

func (shortGuestWriter) Write(p []byte) (int, error)                             { return len(p) - 1, nil }
func (w shortGuestWriter) WriteContext(_ context.Context, p []byte) (int, error) { return w.Write(p) }
func TestGuestShortOutputRejected(t *testing.T) {
	cl, _ := guestFixture(t, []byte("output"), "valid")
	_, err := cl.GuestOperation(context.Background(), app.GuestOperationParams{Operation: "get", IdempotencyKey: "synthetic-request", MaxOutput: 20, Deadline: time.Now().Add(time.Second)}, nil, shortGuestWriter{})
	if err != io.ErrShortWrite {
		t.Fatalf("error=%v", err)
	}
}

func TestGuestRedirectDoesNotReplay(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Redirect(w, r, "/other", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	_, err := New(server.URL, "token").GuestOperation(t.Context(), app.GuestOperationParams{Operation: "prepare", Deadline: time.Now().Add(time.Second)}, nil, nil)
	if err == nil || calls != 1 {
		t.Fatalf("redirect replayed: calls=%d error=%v", calls, err)
	}
}
func TestGuestScriptRejectsInvalidUTF8(t *testing.T) {
	path := filepath.Join(t.TempDir(), "script")
	if err := os.WriteFile(path, []byte{0xff}, 0600); err != nil {
		t.Fatal(err)
	}
	if source, err := OpenGuestPayload(t.Context(), "exec", path); err == nil {
		_ = source.Close()
		t.Fatal("invalid UTF-8 accepted")
	}
}
