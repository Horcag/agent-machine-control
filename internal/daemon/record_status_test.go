package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/daemon"
)

func TestRecordingStatusHTTPDisconnectRetainsInFlightThenTerminal(t *testing.T) {
	entered, release, canceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	captureFinished := make(chan struct{})
	var releaseOnce sync.Once
	releaseCapture := func() { releaseOnce.Do(func() { close(release) }) }
	backend := &consoleDaemonBackend{mockDaemonBackend: &mockDaemonBackend{}, captureHook: func(ctx context.Context) error {
		defer close(captureFinished)
		close(entered)
		select {
		case <-ctx.Done():
		case <-release:
			return ctx.Err()
		}
		close(canceled)
		<-release
		return ctx.Err()
	}}
	endpoint, operator, agent := setupConsoleDaemon(t, backend)
	input := app.ConsoleRecordRequest{Target: "default", RecordingID: "a123456789abcdef0123456789abcdef", Width: 8, Height: 4, Frames: 2, IntervalMillis: 100}
	data, _ := json.Marshal(input)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/v1/console/record", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+agent)
	done := make(chan error, 1)
	clientFinished := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		releaseCapture()
		awaitHTTPRecordingCleanup(t, clientFinished, "HTTP caller")
		select {
		case <-entered:
			awaitHTTPRecordingCleanup(t, captureFinished, "provider capture")
		default:
		}
	})
	go func() {
		defer close(clientFinished)
		response, err := http.DefaultClient.Do(req)
		if response != nil {
			_ = response.Body.Close()
		}
		done <- err
	}()
	awaitHTTPRecordingStart(t, entered, done)
	statusReq := app.ConsoleRecordStatusRequest{Target: "default", RecordingID: input.RecordingID}
	query := func() app.ConsoleRecordStatus { return readHTTPRecordingStatus(t, endpoint, agent, statusReq) }

	initial := query()
	assertHTTPRecordingInFlight(t, initial)
	status, _ := doJSONReq(t, http.MethodPost, endpoint+"/v1/console/record/status", operator, statusReq)
	if status == http.StatusOK {
		t.Fatal("foreign authenticated caller read status")
	}
	cancel()
	awaitHTTPRecordingEvent(t, canceled, "provider cancellation")
	if err := awaitHTTPRecordingResult(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("original canceled response %v", err)
	}
	inFlight := query()
	assertHTTPRecordingInFlight(t, inFlight)
	releaseCapture()
	// The HTTP producer finishes asynchronously after the fake provider returns.
	// Retry only metadata reads; never replay the recording request.
	terminal := awaitRecordingTerminal(t, endpoint, agent, statusReq)
	if !terminal.Terminal || terminal.CaptureInFlight || terminal.TerminalReason != "canceled" || terminal.AttemptedCaptures != 1 || terminal.CompletedCaptures != 0 {
		t.Fatalf("terminal %+v", terminal)
	}
	if stable := query(); stable != terminal {
		t.Fatal("terminal metadata changed")
	}
}

func awaitRecordingTerminal(t *testing.T, endpoint, agent string, statusReq app.ConsoleRecordStatusRequest) app.ConsoleRecordStatus {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	terminal, err := pollRecordingTerminal(ctx, endpoint, agent, statusReq)
	if err != nil {
		t.Fatal(err)
	}
	return terminal
}

func pollRecordingTerminal(ctx context.Context, endpoint, agent string, statusReq app.ConsoleRecordStatusRequest) (app.ConsoleRecordStatus, error) {
	data, err := json.Marshal(statusReq)
	if err != nil {
		return app.ConsoleRecordStatus{}, err
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var last app.ConsoleRecordStatus
	for {
		next, err := queryRecordingTerminalStatus(ctx, endpoint, agent, data)
		if err != nil {
			return last, fmt.Errorf("terminal status read (last %+v): %w", last, err)
		}
		if next != nil {
			last = *next
		}
		if last.Terminal {
			return last, nil
		}
		select {
		case <-ctx.Done():
			return last, fmt.Errorf("terminal status did not settle (last %+v): %w", last, ctx.Err())
		case <-ticker.C:
		}
	}
}

// A nil status means the endpoint explicitly reported inconclusive metadata.
func queryRecordingTerminalStatus(ctx context.Context, endpoint, agent string, data []byte) (*app.ConsoleRecordStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/v1/console/record/status", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+agent)
	req.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	body, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, fmt.Errorf("terminal status body: %w", err)
	}
	if response.StatusCode == http.StatusOK {
		var status app.ConsoleRecordStatus
		if err := json.Unmarshal(body, &status); err != nil {
			return nil, fmt.Errorf("invalid terminal status %s: %w", body, err)
		}
		return &status, nil
	}
	if response.StatusCode == http.StatusConflict {
		var failure daemon.ErrorEnvelope
		if json.Unmarshal(body, &failure) == nil && failure.Error.Category == "recording_status_inconclusive" && failure.Error.Message == "recording status is inconclusive" {
			return nil, nil
		}
	}
	return nil, fmt.Errorf("unexpected terminal status %d: %s", response.StatusCode, body)
}

func readHTTPRecordingStatus(t *testing.T, endpoint, agent string, req app.ConsoleRecordStatusRequest) app.ConsoleRecordStatus {
	t.Helper()
	status, body := doJSONReq(t, http.MethodPost, endpoint+"/v1/console/record/status", agent, req)
	var out app.ConsoleRecordStatus
	if status != http.StatusOK || json.Unmarshal(body, &out) != nil {
		t.Fatalf("query %d %s", status, body)
	}
	return out
}

func TestRecordingStatusHTTPInconclusiveCategoryIsRedacted(t *testing.T) {
	backend := &consoleDaemonBackend{mockDaemonBackend: &mockDaemonBackend{}}
	endpoint, _, agent := setupConsoleDaemon(t, backend)
	status, body := doJSONReq(t, http.MethodPost, endpoint+"/v1/console/record/status", agent, app.ConsoleRecordStatusRequest{Target: "default", RecordingID: "a123456789abcdef0123456789abcdef"})
	if status != http.StatusConflict {
		t.Fatalf("status %d %s", status, body)
	}
	var envelope struct {
		Error struct {
			Category string `json:"category"`
			Message  string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Error.Category != "recording_status_inconclusive" || envelope.Error.Message != "recording status is inconclusive" {
		t.Fatalf("unredacted status failure %s", body)
	}
}

func assertHTTPRecordingInFlight(t *testing.T, status app.ConsoleRecordStatus) {
	t.Helper()
	if status.Terminal || !status.CaptureInFlight || status.AttemptedCaptures != 1 || status.CompletedCaptures != 0 || status.TerminalReason != "" {
		t.Fatalf("blocked capture status %+v", status)
	}
}

func awaitHTTPRecordingEvent(t *testing.T, event <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not occur", name)
	}
}

func awaitHTTPRecordingCleanup(t *testing.T, event <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(5 * time.Second):
		t.Errorf("owned %s did not stop during cleanup", name)
	}
}

func awaitHTTPRecordingStart(t *testing.T, entered <-chan struct{}, done <-chan error) {
	t.Helper()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("caller ended before capture: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP capture did not begin")
	}
}

func awaitHTTPRecordingResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("original caller did not stop")
		return nil
	}
}

func TestPollRecordingTerminal(t *testing.T) {
	t.Run("inconclusive then in-flight then terminal", testRecordingTerminalTransition)
	testRecordingTerminalFailures(t)
	testRecordingTerminalBounds(t)
}

func testRecordingTerminalTransition(t *testing.T) {
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/console/record/status" || r.Method != http.MethodPost {
			t.Errorf("unexpected recording replay: %s %s", r.Method, r.URL.Path)
		}
		read := reads.Add(1)
		switch {
		case read == 1:
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"error":{"category":"recording_status_inconclusive","message":"recording status is inconclusive"}}`)
		case read <= 102:
			_ = json.NewEncoder(w).Encode(app.ConsoleRecordStatus{CaptureInFlight: true, AttemptedCaptures: 1})
		default:
			_ = json.NewEncoder(w).Encode(app.ConsoleRecordStatus{Terminal: true, TerminalReason: "canceled", AttemptedCaptures: 1})
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	status, err := pollRecordingTerminal(ctx, server.URL, "synthetic", app.ConsoleRecordStatusRequest{})
	if err != nil || !status.Terminal || status.TerminalReason != "canceled" || status.AttemptedCaptures != 1 {
		t.Fatalf("terminal %+v, error %v", status, err)
	}
}

func testRecordingTerminalFailures(t *testing.T) {
	t.Helper()
	for _, test := range []struct {
		name string
		code int
		body string
	}{
		{"authorization failure", http.StatusForbidden, `{"error":{"category":"forbidden"}}`},
		{"unrelated conflict", http.StatusConflict, `{"error":{"category":"conflict"}}`},
		{"malformed metadata", http.StatusOK, `{`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var reads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reads.Add(1)
				w.WriteHeader(test.code)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if _, err := pollRecordingTerminal(ctx, server.URL, "synthetic", app.ConsoleRecordStatusRequest{}); err == nil {
				t.Fatal("invalid response was accepted")
			}
			if reads.Load() != 1 {
				t.Fatalf("unexpected response was retried %d times", reads.Load())
			}
		})
	}
}

func testRecordingTerminalBounds(t *testing.T) {
	t.Helper()
	t.Run("deadline bounds a blocked status read", func(t *testing.T) {
		release := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-release:
			}
		}))
		defer server.Close()
		defer close(release)
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		if _, err := pollRecordingTerminal(ctx, server.URL, "synthetic", app.ConsoleRecordStatusRequest{}); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocked status read error %v, want deadline exceeded", err)
		}
	})
	t.Run("cancellation bounds an in-flight producer", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(app.ConsoleRecordStatus{CaptureInFlight: true, AttemptedCaptures: 1})
			cancel()
		}))
		defer server.Close()
		if _, err := pollRecordingTerminal(ctx, server.URL, "synthetic", app.ConsoleRecordStatusRequest{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("unsettled producer error %v, want cancellation", err)
		}
	})
}
