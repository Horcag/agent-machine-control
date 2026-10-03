package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func (c *Client) DesktopAction(ctx context.Context, req app.DesktopActionRequest) (app.DesktopActionResult, error) {
	var out app.DesktopActionResult
	err := c.doRequest(ctx, http.MethodPost, "/v1/desktop/action", req, &out)
	if err != nil && out.Receipt != nil && (out.Receipt.IdempotencyKey != req.IdempotencyKey || req.Request.ObserveOnly()) {
		return app.DesktopActionResult{}, fmt.Errorf("%w: desktop receipt does not match request; effects are unknown", ErrMalformedResponse)
	}
	if err != nil && out.Receipt != nil {
		locator, parseErr := domain.ParseMachineLocator(string(out.Receipt.Target))
		requestedLocator, locatorErr := domain.ParseMachineLocator(req.Target)
		if parseErr != nil || (locatorErr == nil && requestedLocator != locator) || (domain.ValidateMachineGUID(req.Target) == nil && req.Target != locator.VMID) {
			return app.DesktopActionResult{}, fmt.Errorf("%w: desktop receipt target does not match request; effects are unknown", ErrMalformedResponse)
		}
	}
	return out, err
}

func mapDesktopHTTPError(resp *http.Response, out *app.DesktopActionResult) error {
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return err
	}
	var env daemon.DesktopErrorEnvelope
	if err != nil || len(data) > 64*1024 || json.Unmarshal(data, &env) != nil || env.Error.Category == "" {
		return fmt.Errorf("%w: invalid desktop error response; effects are unknown", ErrMalformedResponse)
	}
	if env.ValidateReceipt(resp.StatusCode) != nil {
		env.Error.Message = "invalid desktop failure receipt; effects are unknown"
		return errors.Join(fmt.Errorf("%w: invalid desktop failure receipt; effects are unknown", ErrMalformedResponse), mappedHTTPError(resp.StatusCode, env.ErrorEnvelope))
	}
	out.Receipt, out.CachedReceipt = env.Receipt, env.CachedReceipt
	return mappedHTTPError(resp.StatusCode, env.ErrorEnvelope)
}

type ConsoleLabGrantResult struct {
	Grant   app.ConsoleLabGrant `json:"grant"`
	Receipt domain.Receipt      `json:"receipt"`
}

func (c *Client) IssueConsoleLabGrant(ctx context.Context, req app.ConsoleLabGrantIssueRequest) (ConsoleLabGrantResult, error) {
	var out ConsoleLabGrantResult
	err := c.doRequest(ctx, http.MethodPost, "/v1/desktop/lab/issue", req, &out)
	return out, err
}

func (c *Client) ConsoleLabGrantStatus(ctx context.Context, id string) (app.ConsoleLabGrantStatus, error) {
	var out app.ConsoleLabGrantStatus
	err := c.doRequest(ctx, http.MethodPost, "/v1/desktop/lab/status", struct {
		GrantID string `json:"grant_id"`
	}{id}, &out)
	return out, err
}

func (c *Client) ActiveConsoleLabGrant(ctx context.Context, target string) (app.ConsoleLabGrantStatus, error) {
	var out app.ConsoleLabGrantStatus
	err := c.doRequest(ctx, http.MethodPost, "/v1/desktop/lab/active", struct {
		Target string `json:"target,omitempty"`
	}{target}, &out)
	return out, err
}

func (c *Client) RevokeConsoleLabGrant(ctx context.Context, req app.ConsoleLabGrantRevokeRequest) (domain.Receipt, error) {
	var out domain.Receipt
	err := c.doRequest(ctx, http.MethodPost, "/v1/desktop/lab/revoke", req, &out)
	return out, err
}
