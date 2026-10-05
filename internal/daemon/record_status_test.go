package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
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
	var terminal app.ConsoleRecordStatus
	for range 100 {
		status, body := doJSONReq(t, http.MethodPost, endpoint+"/v1/console/record/status", agent, statusReq)
		if status != http.StatusOK {
			continue
		}
		if json.Unmarshal(body, &terminal) != nil {
			t.Fatalf("invalid terminal status %s", body)
		}
		if terminal.Terminal {
			break
		}
	}
	return terminal
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
