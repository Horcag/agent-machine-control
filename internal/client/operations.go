package client

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/events"
	"github.com/Horcag/agent-machine-control/internal/operations"
)

// IssueOperationApproval calls the operator-only daemon issuance route.
func (c *Client) IssueOperationApproval(ctx context.Context, req daemon.OperationApprovalIssueRequest) (*daemon.OperationApprovalIssueResponse, error) {
	var response daemon.OperationApprovalIssueResponse
	if err := c.doRequest(ctx, http.MethodPost, "/v1/operation-approvals", req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

// CreateOperation submits a new mutation operation to the daemon.
func (c *Client) CreateOperation(ctx context.Context, req daemon.CreateOperationRequest) (*daemon.OperationDTO, error) {
	var dto daemon.OperationDTO
	if err := c.doRequest(ctx, http.MethodPost, "/v1/operations", req, &dto); err != nil {
		return nil, err
	}
	return &dto, nil
}

// GetOperation fetches details for a specific operation.
func (c *Client) GetOperation(ctx context.Context, opID string) (*daemon.OperationDTO, error) {
	if err := domain.ValidateOperationID(opID); err != nil {
		return nil, err
	}
	var dto daemon.OperationDTO
	if err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/v1/operations/%s", opID), nil, &dto); err != nil {
		return nil, err
	}
	return &dto, nil
}

// ListOperations queries operations matching the provided filters.
func (c *Client) ListOperations(ctx context.Context, opts operations.ListOptions) ([]daemon.OperationDTO, error) {
	params := url.Values{}
	if opts.State != "" {
		params.Set("state", string(opts.State))
	}
	if opts.Machine != "" {
		params.Set("machine", string(opts.Machine))
	}
	if opts.Limit > 0 {
		params.Set("limit", strconv.Itoa(opts.Limit))
	}

	path := "/v1/operations"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}

	var resp daemon.OperationListResponse
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Operations, nil
}

// CancelOperation requests cancellation of an in-flight operation.
func (c *Client) CancelOperation(ctx context.Context, opID, reason string) (*daemon.CancelOperationResponse, error) {
	if err := domain.ValidateOperationID(opID); err != nil {
		return nil, err
	}
	body := daemon.CancelOperationRequest{Reason: reason}
	var resp daemon.CancelOperationResponse
	if err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/v1/operations/%s/cancel", opID), body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// WatchEvents opens an SSE stream for an operation, delivering replayed and live events.
func (c *Client) WatchEvents(ctx context.Context, opID string, afterSeq uint64) (<-chan domain.Event, <-chan error, func(), error) {
	if err := domain.ValidateOperationID(opID); err != nil {
		return nil, nil, nil, err
	}

	path := fmt.Sprintf("%s/v1/operations/%s/events", c.endpoint, opID)
	if afterSeq > 0 {
		path += fmt.Sprintf("?after_seq=%d", afterSeq)
	}

	reqCtx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, path, nil)
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token))
	req.Header.Set("Accept", "text/event-stream")
	if afterSeq > 0 {
		req.Header.Set("Last-Event-ID", strconv.FormatUint(afterSeq, 10))
	}

	streamClient := &http.Client{Timeout: 0}
	resp, err := streamClient.Do(req)
	if err != nil {
		cancel()
		return nil, nil, nil, fmt.Errorf("%w: %v", ErrDaemonUnavailable, err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		cancel()
		return nil, nil, nil, mapHTTPError(resp)
	}

	eventsCh := make(chan domain.Event, 64)
	errCh := make(chan error, 1)

	go readSSEStream(reqCtx, resp.Body, eventsCh, errCh)

	return eventsCh, errCh, cancel, nil
}

func readSSEStream(ctx context.Context, body io.ReadCloser, eventsCh chan<- domain.Event, errCh chan<- error) {
	defer close(eventsCh)
	defer close(errCh)
	defer body.Close()

	reader := bufio.NewReader(body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err != io.EOF && ctx.Err() == nil {
				errCh <- err
			}
			return
		}

		if processSSELine(ctx, line, eventsCh) {
			return
		}
	}
}

func processSSELine(ctx context.Context, line string, eventsCh chan<- domain.Event) bool {
	line = strings.TrimSpace(line)
	dataStr, ok := strings.CutPrefix(line, "data:")
	if !ok {
		return false
	}
	dataStr = strings.TrimSpace(dataStr)
	ev, parseErr := events.ParseSSE([]byte(dataStr))
	if parseErr != nil {
		return false
	}
	select {
	case eventsCh <- ev:
	case <-ctx.Done():
		return true
	}
	return ev.State.IsTerminal() || ev.EventType == "overflow"
}

// WaitOperation blocks event-driven until an operation reaches a terminal state.
func (c *Client) WaitOperation(ctx context.Context, opID string, timeout time.Duration, afterSeq uint64) (*daemon.OperationDTO, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Initial check
	initial, err := c.GetOperation(waitCtx, opID)
	if err != nil {
		return nil, err
	}
	if initial.State == "completed" || initial.State == "failed" || initial.State == "cancelled" {
		return initial, nil
	}

	eventsCh, errCh, unsub, err := c.WatchEvents(waitCtx, opID, afterSeq)
	if err != nil {
		return nil, err
	}
	defer unsub()

	for {
		select {
		case <-waitCtx.Done():
			return nil, fmt.Errorf("%w: deadline exceeded waiting for operation %s", ErrTimeout, opID)
		case err, ok := <-errCh:
			if ok && err != nil {
				return nil, fmt.Errorf("events stream error: %w", err)
			}
		case ev, ok := <-eventsCh:
			if !ok {
				// Stream ended, re-fetch final state
				return c.GetOperation(ctx, opID)
			}
			if ev.State.IsTerminal() {
				return c.GetOperation(ctx, opID)
			}
		}
	}
}
