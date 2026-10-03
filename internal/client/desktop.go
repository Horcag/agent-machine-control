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
	if err != nil {
		if mismatch := validateFailureRequest(out.Receipt, req.Target, req.IdempotencyKey, req.Request.ObserveOnly()); mismatch != nil {
			return app.DesktopActionResult{}, errors.Join(err, mismatch)
		}
	}
	return out, err
}

func validateFailureRequest(rcpt *domain.Receipt, target, key string, observe bool) error {
	if rcpt == nil {
		return nil
	}
	if rcpt.IdempotencyKey != key || observe {
		return fmt.Errorf("%w: desktop receipt does not match request; effects are unknown", ErrMalformedResponse)
	}
	locator, parseErr := domain.ParseMachineLocator(string(rcpt.Target))
	requested, locatorErr := domain.ParseMachineLocator(target)
	if parseErr != nil || (locatorErr == nil && requested != locator) || (domain.ValidateMachineGUID(target) == nil && target != locator.VMID) {
		return fmt.Errorf("%w: desktop receipt target does not match request; effects are unknown", ErrMalformedResponse)
	}
	return nil
}

func mapDesktopHTTPError(resp *http.Response, out *app.DesktopActionResult) error {
	return mapActionHTTPError(resp, &out.Receipt, &out.CachedReceipt, "desktop.action")
}

func mapActionHTTPError(resp *http.Response, rcpt **domain.Receipt, cached *bool, kind domain.OperationKind) error {
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return err
	}
	var env daemon.DesktopErrorEnvelope
	if err != nil || len(data) > 64*1024 || json.Unmarshal(data, &env) != nil || env.Error.Category == "" {
		return fmt.Errorf("%w: invalid desktop error response; effects are unknown", ErrMalformedResponse)
	}
	if env.ValidateReceiptForOperation(resp.StatusCode, kind) != nil {
		env.Error.Message = "invalid desktop failure receipt; effects are unknown"
		return errors.Join(fmt.Errorf("%w: invalid desktop failure receipt; effects are unknown", ErrMalformedResponse), mappedHTTPError(resp.StatusCode, env.ErrorEnvelope))
	}
	*rcpt, *cached = env.Receipt, env.CachedReceipt
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
