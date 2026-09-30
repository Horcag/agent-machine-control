package client

import (
	"context"
	"errors"
	"io"
	"net/http"
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
				if !ok || remaining > tc.want || remaining < tc.want-100*time.Millisecond {
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
