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
	"github.com/Horcag/agent-machine-control/internal/domain"
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
		httpClient: &http.Client{},
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
	// Use the caller's operation deadline end-to-end. Only requests without a
	// deadline receive the default bound; a custom HTTP client can shorten it.
	if _, bounded := ctx.Deadline(); !bounded {
		timeout := 90 * time.Second
		if c.httpClient.Timeout > 0 {
			timeout = c.httpClient.Timeout
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

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
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%w: %w", ErrTimeout, context.DeadlineExceeded)
		}
		return fmt.Errorf("%w: %v", ErrDaemonUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return mapHTTPError(resp)
	}

	return decodeHTTPResponse(ctx, resp.Body, out)
}

// decodeHTTPResponse owns strict response parsing and preserves body-read cancellation.
func decodeHTTPResponse(ctx context.Context, body io.Reader, out any) error {
	if out == nil {
		return nil
	}
	responseLimit := int64(1 << 20)
	if _, consoleFrame := out.(*domain.ConsoleFrame); consoleFrame {
		// One million RGBA pixels plus PNG/base64 framing fit within this bound.
		responseLimit = 8 << 20
	}
	dec := json.NewDecoder(io.LimitReader(body, responseLimit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%w: %w", ErrTimeout, context.DeadlineExceeded)
		}
		return fmt.Errorf("%w: %v", ErrMalformedResponse, err)
	}
	return nil
}

func mapHTTPError(resp *http.Response) error {
	var env daemon.ErrorEnvelope
	dec := json.NewDecoder(io.LimitReader(resp.Body, 64*1024))
	if err := dec.Decode(&env); errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return err
	}

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
		return fmt.Errorf("%w: %w", ErrInvalidArgument, apiErr)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: %w", ErrDenied, apiErr)
	case http.StatusNotFound:
		return fmt.Errorf("%w: %w", ErrNotFound, apiErr)
	case http.StatusConflict:
		return fmt.Errorf("%w: %w", ErrConflict, apiErr)
	case http.StatusGatewayTimeout:
		return fmt.Errorf("%w: %w", ErrTimeout, apiErr)
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable:
		return fmt.Errorf("%w: %w", ErrDaemonUnavailable, apiErr)
	default:
		if resp.StatusCode >= 500 {
			return fmt.Errorf("%w: %w", ErrMalformedResponse, apiErr)
		}
		return apiErr
	}
}
