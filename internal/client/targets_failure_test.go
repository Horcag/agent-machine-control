package client

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/daemon"
)

type targetRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn targetRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestTargetClientMethodsReturnTransportFailures(t *testing.T) {
	synthetic := errors.New("synthetic target transport failure")
	cl := New("http://127.0.0.1:1", "synthetic-token", WithHTTPClient(&http.Client{
		Transport: targetRoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, synthetic }),
	}))
	checks := []func() error{
		func() error {
			_, err := cl.IssueTargetApproval(context.Background(), daemon.TargetApprovalIssueRequest{})
			return err
		},
		func() error { _, err := cl.GetTarget(context.Background()); return err },
		func() error {
			_, err := cl.EnrollTarget(context.Background(), daemon.TargetMutationRequest{})
			return err
		},
		func() error {
			_, err := cl.ClearTarget(context.Background(), daemon.TargetMutationRequest{})
			return err
		},
	}
	for index, check := range checks {
		if err := check(); !errors.Is(err, ErrDaemonUnavailable) {
			t.Fatalf("target client check %d error = %v", index, err)
		}
	}
}
