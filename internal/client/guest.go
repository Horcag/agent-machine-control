package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
)

// GuestOperation never retries. Output must be an owned unpublished sink: a
// response is accepted only after EOF, the final receipt, size and digest agree.
func (c *Client) GuestOperation(ctx context.Context, p app.GuestOperationParams, input stream.ContextReader, output stream.ContextWriter) (app.GuestOperationResult, error) {
	var zero app.GuestOperationResult
	prefix, err := encodeGuestMetadata(p)
	if err != nil {
		return zero, err
	}
	ctx, cancel := context.WithDeadline(ctx, p.Deadline)
	defer cancel()
	var payload io.Reader = bytes.NewReader(nil)
	if p.Size > 0 {
		if input == nil {
			return zero, ErrInvalidArgument
		}
		payload = guestRequestReader{ctx, input}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/v1/guest/operations", io.MultiReader(bytes.NewReader(prefix), payload))
	if err != nil {
		return zero, err
	}
	req.ContentLength = int64(len(prefix)) + p.Size
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", daemon.GuestContentType)
	resp, err := c.guestHTTPClient().Do(req)
	if err != nil {
		return zero, fmt.Errorf("%w: %v", app.ErrGuestUncertain, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return zero, mapHTTPError(resp)
	}
	if resp.Header.Get("Content-Type") != "application/octet-stream" {
		return zero, ErrMalformedResponse
	}
	total, digest, err := readGuestHTTPOutput(ctx, resp, cancel, p.MaxOutput, output)
	if err != nil {
		return zero, err
	}
	final, err := decodeGuestFinal(resp)
	if err != nil {
		return zero, err
	}
	if final.Error != "" {
		return final.Result, fmt.Errorf("%w: %s", app.ErrGuestUncertain, final.Error)
	}
	if err = validateGuestFinal(p, final.Result, total, digest); err != nil {
		return final.Result, err
	}

	return final.Result, nil
}

type guestRequestReader struct {
	ctx    context.Context
	source stream.ContextReader
}

func (r guestRequestReader) Read(p []byte) (int, error) { return r.source.ReadContext(r.ctx, p) }
func (c *Client) IssueGuestApproval(ctx context.Context, req daemon.GuestApprovalRequest) (*daemon.GuestApprovalResponse, error) {
	var result daemon.GuestApprovalResponse
	bounded := *c
	bounded.httpClient = c.guestHTTPClient()
	err := bounded.doRequest(ctx, http.MethodPost, "/v1/guest/approvals", req, &result)
	return &result, err
}

func readGuestHTTPOutput(ctx context.Context, resp *http.Response, cancel context.CancelFunc, limit int64, output stream.ContextWriter) (int64, string, error) {
	source := stream.HTTPInput(resp.Body, cancel)
	digest := sha256.New()
	buf := make([]byte, stream.BufferSize)
	var total int64
	for {
		n, readErr := source.ReadContext(ctx, buf)
		if n > 0 {
			total += int64(n)
			if total > limit {
				return total, "", ErrMalformedResponse
			}
			_, _ = digest.Write(buf[:n])
			if output == nil {
				return total, "", ErrInvalidArgument
			}
			written, err := output.WriteContext(ctx, buf[:n])
			if err != nil {
				return total, "", err
			}
			if written != n {
				return total, "", io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return total, "", readErr
		}
	}
	return total, hex.EncodeToString(digest.Sum(nil)), nil
}
func validateGuestFinal(p app.GuestOperationParams, result app.GuestOperationResult, total int64, digest string) error {
	expectedSize := result.Size
	if p.Operation == "put" {
		expectedSize = 0
		if result.Size != p.Size || result.SHA256 != p.SHA256 {
			return ErrMalformedResponse
		}
	}
	if result.CachedReceipt && (p.Operation == "get" || p.Operation == "exec") {
		return ErrConflict
	}
	if verifyGuestReceipt(p, result.Receipt) != nil || expectedSize != total || ((p.Operation == "get" || p.Operation == "exec") && result.SHA256 != digest) {
		return ErrMalformedResponse
	}
	return nil
}

func decodeGuestFinal(resp *http.Response) (daemon.GuestFinal, error) {
	trailer := resp.Trailer.Get(daemon.GuestResultTrailer)
	if len(trailer) == 0 || len(trailer) > daemon.GuestMetadataLimit*2 {
		return daemon.GuestFinal{}, ErrMalformedResponse
	}
	decoded, err := base64.RawStdEncoding.DecodeString(trailer)
	if err != nil || len(decoded) > daemon.GuestMetadataLimit {
		return daemon.GuestFinal{}, ErrMalformedResponse
	}
	var final daemon.GuestFinal
	dec := json.NewDecoder(bytes.NewReader(decoded))
	dec.DisallowUnknownFields()
	var extra any
	if dec.Decode(&final) != nil || dec.Decode(&extra) != io.EOF {
		return daemon.GuestFinal{}, ErrMalformedResponse
	}
	return final, nil
}

func encodeGuestMetadata(p app.GuestOperationParams) ([]byte, error) {
	data, err := json.Marshal(p)
	if err != nil || len(data) > daemon.GuestMetadataLimit || p.Size < 0 || p.Size > stream.MaxTransferSize || p.Deadline.IsZero() {
		return nil, ErrInvalidArgument
	}
	prefix := make([]byte, 4+len(data))
	// #nosec G115 -- metadata length was bounded to 64KiB before conversion.
	binary.BigEndian.PutUint32(prefix, uint32(len(data)))
	copy(prefix[4:], data)
	return prefix, nil
}
func (c *Client) guestHTTPClient() *http.Client {
	bounded := *c.httpClient
	bounded.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &bounded
}

func verifyGuestReceipt(p app.GuestOperationParams, receipt *domain.Receipt) error {
	if receipt == nil {
		return ErrMalformedResponse
	}
	if err := receipt.Validate(); err != nil {
		return ErrMalformedResponse
	}
	if receipt.Outcome.Status != domain.OutcomeSuccess || receipt.OperationKind != domain.OperationKind("guest."+p.Operation) || receipt.IdempotencyKey != p.IdempotencyKey {
		return ErrMalformedResponse
	}
	return nil
}
