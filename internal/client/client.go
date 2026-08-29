package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/statedir"
)

// TokenType re-exports auth.TokenType for client callers.
type TokenType = auth.TokenType

const (
	TokenTypeOperator = auth.TokenTypeOperator
	TokenTypeAgentMCP = auth.TokenTypeAgentMCP
)

// Option configures Client parameters.
type Option func(*Client)

// WithHTTPClient sets a custom http.Client.
func WithHTTPClient(c *http.Client) Option {
	return func(cl *Client) {
		cl.httpClient = c
	}
}

// Client provides typed HTTP communication with the amcd daemon.
type Client struct {
	endpoint   string
	token      string
	httpClient *http.Client
}

// New creates a new Client configured with the target endpoint and bearer token.
func New(endpoint, token string, opts ...Option) *Client {
	cl := &Client{
		endpoint:   strings.TrimRight(endpoint, "/"),
		token:      strings.TrimSpace(token),
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
	for _, opt := range opts {
		opt(cl)
	}
	return cl
}

// Discover resolves the active daemon endpoint and reads credentials from the state directory.
func Discover(stateDirPath string, tokenType TokenType) (*Client, error) {
	sd, err := statedir.Resolve(stateDirPath)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to resolve state directory: %v", ErrDaemonUnavailable, err)
	}

	rec, err := daemon.ReadEndpointFile(sd.DaemonDir())
	if err != nil {
		return nil, fmt.Errorf("%w: daemon endpoint file missing or unreadable: %v", ErrDaemonUnavailable, err)
	}

	token, err := auth.ReadTokenFile(sd.AuthDir(), tokenType)
	if err != nil {
		return nil, fmt.Errorf("%w: auth token missing: %v", ErrDenied, err)
	}

	return New(rec.Endpoint, token), nil
}

// Endpoint returns the configured daemon server URL.
func (c *Client) Endpoint() string {
	return c.endpoint
}

func (c *Client) doRequest(ctx context.Context, method, path string, body any, out any) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("%w: marshal request body failed: %v", ErrInvalidArgument, err)
		}
		bodyReader = bytes.NewReader(data)
	}

	url := fmt.Sprintf("%s%s", c.endpoint, path)
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("%w: failed to construct request: %v", ErrInvalidArgument, err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %v", ErrDaemonUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return mapHTTPError(resp)
	}

	if out != nil {
		dec := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
		dec.DisallowUnknownFields()
		if err := dec.Decode(out); err != nil {
			return fmt.Errorf("%w: %v", ErrMalformedResponse, err)
		}
	}

	return nil
}

func mapHTTPError(resp *http.Response) error {
	var env daemon.ErrorEnvelope
	dec := json.NewDecoder(io.LimitReader(resp.Body, 64*1024))
	_ = dec.Decode(&env)

	msg := env.Error.Message
	if msg == "" {
		msg = fmt.Sprintf("daemon returned HTTP %d", resp.StatusCode)
	}
	cat := env.Error.Category

	apiErr := &APIError{
		StatusCode: resp.StatusCode,
		Category:   cat,
		Message:    msg,
	}

	switch resp.StatusCode {
	case http.StatusBadRequest:
		return fmt.Errorf("%w: %s", ErrInvalidArgument, msg)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: %s", ErrDenied, msg)
	case http.StatusNotFound:
		return fmt.Errorf("%w: %s", ErrNotFound, msg)
	case http.StatusConflict:
		return fmt.Errorf("%w: %s", ErrConflict, msg)
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return fmt.Errorf("%w: %s", ErrDaemonUnavailable, msg)
	default:
		if resp.StatusCode >= 500 {
			return fmt.Errorf("%w: %s", ErrMalformedResponse, msg)
		}
		return apiErr
	}
}
