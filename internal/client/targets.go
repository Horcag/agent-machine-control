package client

import (
	"context"
	"net/http"

	"github.com/Horcag/agent-machine-control/internal/daemon"
)

// IssueTargetApproval asks amcd to prepare and approve one exact target authority transition.
func (c *Client) IssueTargetApproval(ctx context.Context, req daemon.TargetApprovalIssueRequest) (*daemon.TargetApprovalIssueResponse, error) {
	var response daemon.TargetApprovalIssueResponse
	if err := c.doRequest(ctx, http.MethodPost, "/v1/target-approvals", req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

// GetTarget returns the freshly resolved enrolled target.
func (c *Client) GetTarget(ctx context.Context) (*daemon.TargetResponse, error) {
	var response daemon.TargetResponse
	if err := c.doRequest(ctx, http.MethodGet, "/v1/target", nil, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

// EnrollTarget executes one exact approved target enrollment.
func (c *Client) EnrollTarget(ctx context.Context, req daemon.TargetMutationRequest) (*daemon.TargetResponse, error) {
	var response daemon.TargetResponse
	if err := c.doRequest(ctx, http.MethodPut, "/v1/target", req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}

// ClearTarget executes one exact approved target clear.
func (c *Client) ClearTarget(ctx context.Context, req daemon.TargetMutationRequest) (*daemon.TargetResponse, error) {
	var response daemon.TargetResponse
	if err := c.doRequest(ctx, http.MethodDelete, "/v1/target", req, &response); err != nil {
		return nil, err
	}
	return &response, nil
}
