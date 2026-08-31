package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/auth"
)

func TestAuthenticatedSessionOpenQueuedAcrossShutdownIsRejectedAfterSemaphore(t *testing.T) {
	dir := missingDaemonStateRoot(t)
	srv, err := NewServer(Config{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })

	srv.semaphore = make(chan struct{}, 1)
	srv.semaphore <- struct{}{}
	passedEarlyCheck := make(chan struct{})
	var signalOnce sync.Once
	srv.afterEarlyMutationAdmissionCheck = func() {
		signalOnce.Do(func() { close(passedEarlyCheck) })
	}

	var channelEffects, activeRecords, receipts, mutationEffects atomic.Int32
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		channelEffects.Add(1)
		activeRecords.Add(1)
		receipts.Add(1)
		mutationEffects.Add(1)
	})
	handler := srv.authMiddleware(next)
	token, err := auth.ReadTokenFile(filepath.Join(dir, "auth"), auth.TokenTypeOperator)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{"target":"c4a523d4-6b99-4d62-a5e2-4752c0f20001"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(recorder, req)
		close(done)
	}()

	<-passedEarlyCheck
	select {
	case <-done:
		t.Fatal("session.open did not wait for the occupied semaphore")
	default:
	}
	srv.TriggerShutdown()
	<-srv.semaphore
	<-done

	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), `"category":"shutting_down"`) {
		t.Fatalf("queued session.open response = %d %s, want shutting_down", recorder.Code, recorder.Body.String())
	}
	if channelEffects.Load() != 0 || activeRecords.Load() != 0 || receipts.Load() != 0 || mutationEffects.Load() != 0 {
		t.Fatalf("post-cutover effects = channel %d active %d receipts %d mutation %d, want all zero",
			channelEffects.Load(), activeRecords.Load(), receipts.Load(), mutationEffects.Load())
	}
}
