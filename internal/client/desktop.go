package client

import (
	"context"
	"net/http"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func (c *Client) DesktopAction(ctx context.Context, req app.DesktopActionRequest) (app.DesktopActionResult, error) {
	var out app.DesktopActionResult
	err := c.doRequest(ctx, http.MethodPost, "/v1/desktop/action", req, &out)
	return out, err
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
