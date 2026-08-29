package client

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/events"
	"github.com/Horcag/agent-machine-control/internal/receipt"
)

// Health returns daemon runtime health metadata.
func (c *Client) Health(ctx context.Context) (*daemon.HealthResponse, error) {
	var resp daemon.HealthResponse
	if err := c.doRequest(ctx, http.MethodGet, "/v1/health", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// GetAudit retrieves recent audit events from the daemon.
func (c *Client) GetAudit(ctx context.Context, limit int) ([]audit.Event, error) {
	path := "/v1/audit"
	if limit > 0 {
		path += fmt.Sprintf("?limit=%s", strconv.Itoa(limit))
	}

	var resp daemon.AuditListResponse
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Events, nil
}

// GetReceipt retrieves an execution receipt by ID.
func (c *Client) GetReceipt(ctx context.Context, receiptID string) (*receipt.DTO, error) {
	if err := domain.ValidateReceiptID(receiptID); err != nil {
		return nil, err
	}

	var resp daemon.ReceiptResponse
	if err := c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/v1/receipts/%s", receiptID), nil, &resp); err != nil {
		return nil, err
	}
	return &resp.Receipt, nil
}

// StopDaemon sends a graceful shutdown request to the daemon.
func (c *Client) StopDaemon(ctx context.Context) (*daemon.StopDaemonResponse, error) {
	var resp daemon.StopDaemonResponse
	if err := c.doRequest(ctx, http.MethodPost, "/v1/daemon/stop", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// WatchGlobalEvents opens an SSE stream for global daemon events (/v1/events).
func (c *Client) WatchGlobalEvents(ctx context.Context) (<-chan domain.Event, <-chan error, func(), error) {
	path := fmt.Sprintf("%s/v1/events", c.endpoint)

	reqCtx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, path, nil)
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token))
	req.Header.Set("Accept", "text/event-stream")

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

	go readGlobalSSEStream(reqCtx, resp.Body, eventsCh, errCh)

	return eventsCh, errCh, cancel, nil
}

func readGlobalSSEStream(ctx context.Context, body io.ReadCloser, eventsCh chan<- domain.Event, errCh chan<- error) {
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

		line = strings.TrimSpace(line)
		dataStr, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		dataStr = strings.TrimSpace(dataStr)
		ev, parseErr := events.ParseSSE([]byte(dataStr))
		if parseErr != nil {
			continue
		}
		select {
		case eventsCh <- ev:
		case <-ctx.Done():
			return
		}
		if ev.EventType == "overflow" {
			return
		}
	}
}
