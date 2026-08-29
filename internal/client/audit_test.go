package client_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/events"
)

func TestClient_WatchGlobalEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/events" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			return
		}

		ev1 := domain.Event{
			Sequence:    1,
			OperationID: "op-00000000000000000000000000000001",
			EventType:   "state_change",
			State:       domain.OpStateRunning,
			Timestamp:   time.Now().UTC(),
		}
		data1, _ := events.FormatSSE(ev1)
		_, _ = w.Write(data1)
		flusher.Flush()

		time.Sleep(20 * time.Millisecond)

		evOverflow := domain.Event{
			Sequence:    2,
			OperationID: "op-00000000000000000000000000000001",
			EventType:   "overflow",
			Timestamp:   time.Now().UTC(),
		}
		dataOverflow, _ := events.FormatSSE(evOverflow)
		_, _ = w.Write(dataOverflow)
		flusher.Flush()
	}))
	defer srv.Close()

	cl := client.New(srv.URL, "test-token", client.WithHTTPClient(srv.Client()))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	eventsCh, errCh, unsub, err := cl.WatchGlobalEvents(ctx)
	if err != nil {
		t.Fatalf("WatchGlobalEvents failed: %v", err)
	}
	defer unsub()

	var received []domain.Event
	for ev := range eventsCh {
		received = append(received, ev)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("unexpected error on errCh: %v", err)
		}
	default:
	}

	if len(received) != 2 {
		t.Errorf("expected 2 events, got %d", len(received))
	}
}

func TestClient_WatchGlobalEvents_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	cl := client.New(srv.URL, "test-token", client.WithHTTPClient(srv.Client()))

	_, _, _, err := cl.WatchGlobalEvents(context.Background())
	if err == nil {
		t.Fatalf("expected error for 403 on /v1/events")
	}
}
