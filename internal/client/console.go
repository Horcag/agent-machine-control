package client

import (
	"context"
	"net/http"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func (c *Client) ConsoleRecord(ctx context.Context, req app.ConsoleRecordRequest) (app.ConsoleRecording, error) {
	var out app.ConsoleRecording
	err := c.doRequest(ctx, http.MethodPost, "/v1/console/record", req, &out)
	return out, err
}

// ConsoleScreenshot captures the enrolled VM through the authenticated daemon.
func (c *Client) ConsoleScreenshot(ctx context.Context, req app.ConsoleScreenshotRequest) (domain.ConsoleFrame, error) {
	var out domain.ConsoleFrame
	err := c.doRequest(ctx, http.MethodPost, "/v1/console/screenshot", req, &out)
	return out, err
}

// ConsoleInput submits one bounded console mutation through the authenticated daemon.
func (c *Client) ConsoleInput(ctx context.Context, req app.ConsoleInputRequest) (domain.Receipt, error) {
	var out domain.Receipt
	err := c.doRequest(ctx, http.MethodPost, "/v1/console/input", req, &out)
	return out, err
}
