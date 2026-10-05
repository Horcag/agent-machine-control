package client

import (
	"context"
	"errors"
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
	out, err := c.ConsoleInputResult(ctx, req)
	if out.Receipt == nil {
		return domain.Receipt{}, err
	}
	return *out.Receipt, err
}

// ConsoleInputResult retains failure evidence and cache provenance for desktop_act.
func (c *Client) ConsoleInputResult(ctx context.Context, req app.ConsoleInputRequest) (app.ConsoleInputResult, error) {
	var out app.ConsoleInputResult
	err := c.doRequest(ctx, http.MethodPost, "/v1/console/input", req, &out)
	if err != nil {
		if mismatch := validateFailureRequest(out.Receipt, req.Target, req.IdempotencyKey, false); mismatch != nil {
			return app.ConsoleInputResult{}, errors.Join(err, mismatch)
		}
	}
	return out, err
}

func (c *Client) ConsoleRecordStatus(ctx context.Context, req app.ConsoleRecordStatusRequest) (app.ConsoleRecordStatus, error) {
	var out app.ConsoleRecordStatus
	err := c.doRequest(ctx, http.MethodPost, "/v1/console/record/status", req, &out)
	return out, err
}
