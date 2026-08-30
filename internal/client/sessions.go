package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

// SessionDTO re-exports daemon.SessionDTO.
type SessionDTO = daemon.SessionDTO

// SessionChunkDTO re-exports daemon.SessionChunkDTO.
type SessionChunkDTO = daemon.SessionChunkDTO

func firstApproval(approvals []*domain.Approval) *domain.Approval {
	if len(approvals) > 0 {
		return approvals[0]
	}
	return nil
}

// OpenSession establishes a new persistent SSH terminal session on the daemon.
func (c *Client) OpenSession(ctx context.Context, req daemon.SessionOpenRequest) (*daemon.SessionOpenResponse, error) {
	timeout, err := daemon.ResolveSessionTimeout(req.TimeoutSeconds, req.TimeoutMillis, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	reqCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	var resp daemon.SessionOpenResponse
	if err := c.doRequest(reqCtx, http.MethodPost, "/v1/sessions", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ReadSession reads buffered output chunks starting at the given sequence offset.
func (c *Client) ReadSession(ctx context.Context, id string, afterSeq uint64, limitBytes int, timeout time.Duration) (*daemon.SessionReadResponse, error) {
	if err := domain.ValidateSessionID(id); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}

	path := fmt.Sprintf("/v1/sessions/%s/read?after_seq=%d", id, afterSeq)
	if limitBytes > 0 {
		path += fmt.Sprintf("&limit_bytes=%d", limitBytes)
	}

	reqCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	var resp daemon.SessionReadResponse
	if err := c.doRequest(reqCtx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// WriteSession transmits character data to the session stdin.
func (c *Client) WriteSession(ctx context.Context, id string, data string, reason, idempotencyKey string, approval ...*domain.Approval) (*daemon.SessionWriteResponse, error) {
	return c.WriteSessionWithTimeout(ctx, id, data, reason, idempotencyKey, 30*time.Second, approval...)
}

// WriteSessionWithTimeout transmits character data with an explicit end-to-end execution bound.
func (c *Client) WriteSessionWithTimeout(ctx context.Context, id string, data string, reason, idempotencyKey string, timeout time.Duration, approval ...*domain.Approval) (*daemon.SessionWriteResponse, error) {
	return c.writeSessionWithApproval(ctx, id, data, reason, idempotencyKey, timeout, firstApproval(approval), "")
}

// WriteSessionWithApprovalID transmits data using a server-issued approval reference.
func (c *Client) WriteSessionWithApprovalID(ctx context.Context, id string, data string, reason, idempotencyKey string, timeout time.Duration, approvalID string) (*daemon.SessionWriteResponse, error) {
	return c.writeSessionWithApproval(ctx, id, data, reason, idempotencyKey, timeout, nil, approvalID)
}

func (c *Client) writeSessionWithApproval(ctx context.Context, id string, data string, reason, idempotencyKey string, timeout time.Duration, approval *domain.Approval, approvalID string) (*daemon.SessionWriteResponse, error) {
	return timedSessionMutation[daemon.SessionWriteResponse](ctx, c, id, "/write", timeout, func(seconds, millis int64) any {
		return daemon.SessionWriteRequest{
			Data: data, Reason: reason, IdempotencyKey: idempotencyKey,
			TimeoutSeconds: seconds, TimeoutMillis: millis, ApprovalID: approvalID, Approval: approval,
		}
	})
}

// SendControlKey sends a terminal control keystroke or escape code.
func (c *Client) SendControlKey(ctx context.Context, id string, key domain.ControlKey, reason, idempotencyKey string, approval ...*domain.Approval) (*daemon.SessionControlResponse, error) {
	return c.SendControlKeyWithTimeout(ctx, id, key, reason, idempotencyKey, 30*time.Second, approval...)
}

// SendControlKeyWithTimeout sends a control key with an explicit end-to-end execution bound.
func (c *Client) SendControlKeyWithTimeout(ctx context.Context, id string, key domain.ControlKey, reason, idempotencyKey string, timeout time.Duration, approval ...*domain.Approval) (*daemon.SessionControlResponse, error) {
	return c.sendControlKeyWithApproval(ctx, id, key, reason, idempotencyKey, timeout, firstApproval(approval), "")
}

// SendControlKeyWithApprovalID sends a control key using a server-issued approval reference.
func (c *Client) SendControlKeyWithApprovalID(ctx context.Context, id string, key domain.ControlKey, reason, idempotencyKey string, timeout time.Duration, approvalID string) (*daemon.SessionControlResponse, error) {
	return c.sendControlKeyWithApproval(ctx, id, key, reason, idempotencyKey, timeout, nil, approvalID)
}

func (c *Client) sendControlKeyWithApproval(ctx context.Context, id string, key domain.ControlKey, reason, idempotencyKey string, timeout time.Duration, approval *domain.Approval, approvalID string) (*daemon.SessionControlResponse, error) {
	return timedSessionMutation[daemon.SessionControlResponse](ctx, c, id, "/control", timeout, func(seconds, millis int64) any {
		return daemon.SessionControlRequest{
			Key: string(key), Reason: reason, IdempotencyKey: idempotencyKey,
			TimeoutSeconds: seconds, TimeoutMillis: millis, ApprovalID: approvalID, Approval: approval,
		}
	})
}

// WaitSession blocks until the session settles or matches a regex pattern.
func (c *Client) WaitSession(ctx context.Context, id string, req daemon.SessionWaitRequest) (*daemon.SessionWaitResponse, error) {
	if err := domain.ValidateSessionID(id); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}

	timeout, err := daemon.ResolveSessionTimeout(req.TimeoutSeconds, req.TimeoutMillis, 0)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	reqCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	var resp daemon.SessionWaitResponse
	path := fmt.Sprintf("/v1/sessions/%s/wait", id)
	if err := c.doRequest(reqCtx, http.MethodPost, path, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListSessions queries active and recent sessions.
func (c *Client) ListSessions(ctx context.Context, machineRef string) ([]daemon.SessionDTO, error) {
	path := "/v1/sessions"
	if machineRef != "" {
		path = fmt.Sprintf("/v1/sessions?machine=%s", url.QueryEscape(machineRef))
	}

	var resp daemon.SessionListResponse
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Sessions, nil
}

// GetSession retrieves the latest observation of a session.
func (c *Client) GetSession(ctx context.Context, id string) (*daemon.SessionDTO, error) {
	if err := domain.ValidateSessionID(id); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}

	var resp daemon.SessionOpenResponse
	path := fmt.Sprintf("/v1/sessions/%s", id)
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return &resp.Session, nil
}

// CloseSession gracefully terminates a persistent session.
func (c *Client) CloseSession(ctx context.Context, id string, reason, idempotencyKey string, force bool, approval ...*domain.Approval) (*daemon.SessionCloseResponse, error) {
	return c.CloseSessionWithTimeout(ctx, id, reason, idempotencyKey, force, 30*time.Second, approval...)
}

// CloseSessionWithTimeout terminates a session with an explicit end-to-end execution bound.
func (c *Client) CloseSessionWithTimeout(ctx context.Context, id string, reason, idempotencyKey string, force bool, timeout time.Duration, approval ...*domain.Approval) (*daemon.SessionCloseResponse, error) {
	return c.closeSessionWithApproval(ctx, id, reason, idempotencyKey, force, timeout, firstApproval(approval), "")
}

// CloseSessionWithApprovalID closes a session using a server-issued approval reference.
func (c *Client) CloseSessionWithApprovalID(ctx context.Context, id string, reason, idempotencyKey string, force bool, timeout time.Duration, approvalID string) (*daemon.SessionCloseResponse, error) {
	return c.closeSessionWithApproval(ctx, id, reason, idempotencyKey, force, timeout, nil, approvalID)
}

func (c *Client) closeSessionWithApproval(ctx context.Context, id string, reason, idempotencyKey string, force bool, timeout time.Duration, approval *domain.Approval, approvalID string) (*daemon.SessionCloseResponse, error) {
	return timedSessionMutation[daemon.SessionCloseResponse](ctx, c, id, "/close", timeout, func(seconds, millis int64) any {
		return daemon.SessionCloseRequest{
			Reason: reason, IdempotencyKey: idempotencyKey, Force: force,
			TimeoutSeconds: seconds, TimeoutMillis: millis, ApprovalID: approvalID, Approval: approval,
		}
	})
}

func timedSessionMutation[T any](ctx context.Context, c *Client, id, suffix string, timeout time.Duration, buildBody func(int64, int64) any) (*T, error) {
	if err := domain.ValidateSessionID(id); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}

	timeoutSeconds, timeoutMillis, err := daemon.EncodeSessionTimeout(timeout)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	var response T
	reqCtx, cancel := boundedRequestContext(ctx, timeout)
	defer cancel()
	if err := c.doRequest(reqCtx, http.MethodPost, "/v1/sessions/"+id+suffix, buildBody(timeoutSeconds, timeoutMillis), &response); err != nil {
		return nil, err
	}
	return &response, nil
}

func boundedRequestContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}
