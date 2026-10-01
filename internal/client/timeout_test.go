package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTargetRequestBudgetAllowsLocalQueryAndPreservesExplicitBounds(t *testing.T) {
	for _, tc := range []struct {
		name          string
		callerTimeout time.Duration
		clientTimeout time.Duration
		want          time.Duration
	}{
		{"default allows local query", 0, 0, 90 * time.Second},
		{"caller deadline", time.Second, 0, time.Second},
		{"custom HTTP deadline", 0, 2 * time.Second, 2 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cl := New("http://127.0.0.1", "synthetic-token")
			if tc.clientTimeout != 0 {
				cl = New("http://127.0.0.1", "synthetic-token", WithHTTPClient(&http.Client{Timeout: tc.clientTimeout}))
			}
			cl.httpClient.Transport = targetRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				deadline, ok := req.Context().Deadline()
				remaining := time.Until(deadline)
				if !ok || remaining > tc.want || remaining < tc.want-500*time.Millisecond {
					t.Errorf("request budget = %v; want bounded by %v", remaining, tc.want)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
			})
			ctx := context.Background()
			cancel := func() {}
			if tc.callerTimeout != 0 {
				ctx, cancel = context.WithTimeout(ctx, tc.callerTimeout)
			}
			defer cancel()
			if _, err := cl.GetTarget(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTargetRequestStillHonorsCallerCancellation(t *testing.T) {
	cl := New("http://127.0.0.1", "synthetic-token")
	cl.httpClient.Transport = targetRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, req.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cl.GetTarget(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled target request = %v", err)
	}
}

func TestExplicitRequestDeadlineIsNotCutShortByDefaultHTTPTimeout(t *testing.T) {
	cl := New("http://127.0.0.1", "synthetic-token")
	cl.httpClient.Transport = targetRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		if !ok || time.Until(deadline) < 4*time.Minute {
			t.Errorf("lost caller budget: %v", deadline)
		}
		if cl.httpClient.Timeout != 0 {
			t.Errorf("hidden HTTP cap defeats caller deadline: %v", cl.httpClient.Timeout)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	if _, err := cl.GetTarget(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPTimeoutRetainsSemanticCategory(t *testing.T) {
	for _, status := range []int{http.StatusGatewayTimeout, http.StatusServiceUnavailable, http.StatusConflict} {
		response := &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"error":{"category":"timeout","message":"redacted provider deadline"}}`))}
		err := mapHTTPError(response)
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Category != "timeout" || apiErr.StatusCode != status {
			t.Fatalf("lost daemon category: %v", err)
		}
		if status == http.StatusGatewayTimeout && !errors.Is(err, ErrTimeout) {
			t.Fatalf("provider timeout reported as daemon outage: %v", err)
		}
	}
}

func TestResponseBodyRetainsCallerDeadlineAndCancellation(t *testing.T) {
	for _, deadline := range []bool{true, false} {
		t.Run(fmt.Sprint("deadline=", deadline), func(t *testing.T) {
			headers := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				close(headers)
				<-r.Context().Done()
			}))
			defer server.Close()
			ctx := t.Context()
			var cancel context.CancelFunc
			want := context.Canceled
			if deadline {
				ctx, cancel = context.WithTimeout(t.Context(), 500*time.Millisecond)
				want = context.DeadlineExceeded
			} else {
				ctx, cancel = context.WithCancel(ctx)
			}
			defer cancel()
			done := make(chan error, 1)
			go func() {
				var out any
				done <- New(server.URL, "synthetic-token").doRequest(ctx, http.MethodGet, "/", nil, &out)
			}()
			select {
			case <-headers:
			case <-ctx.Done():
				t.Fatal("response headers not received before deadline")
			}
			if !deadline {
				cancel()
			}
			err := <-done
			if !errors.Is(err, want) || errors.Is(err, ErrMalformedResponse) {
				t.Fatalf("body read lost cause: %v", err)
			}
		})
	}
}
