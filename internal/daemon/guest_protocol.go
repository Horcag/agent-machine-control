package daemon

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
)

const GuestMetadataLimit = 64 * 1024
const GuestResultTrailer = "X-Amc-Guest-Result"

// GuestErrorProtocolHeader opts into typed trailer failures; older clients decode
// trailers strictly and receive ordinary HTTP errors for early rejections.
const GuestErrorProtocolHeader = "X-Amc-Guest-Errors"
const GuestErrorProtocolVersion = "categories-v1"
const GuestContentType = "application/vnd.amc.guest-stream"

type GuestFinal struct {
	Result   app.GuestOperationResult `json:"result"`
	Error    string                   `json:"error,omitempty"`
	Category string                   `json:"category,omitempty"`
}
type GuestApprovalRequest struct {
	Params         app.GuestOperationParams `json:"params"`
	Beneficiary    string                   `json:"beneficiary,omitempty"`
	ValidForMillis int64                    `json:"valid_for_millis"`
}
type GuestApprovalResponse struct {
	Grant   *app.SessionApprovalGrant `json:"grant"`
	Receipt *domain.Receipt           `json:"receipt"`
}

func DecodeGuestMetadata(r io.Reader) (app.GuestOperationParams, int64, error) {
	var p app.GuestOperationParams
	var size uint32
	if err := binary.Read(r, binary.BigEndian, &size); err != nil {
		return p, 0, err
	}
	if size == 0 || size > GuestMetadataLimit {
		return p, 0, stream.ErrRequest
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return p, 0, err
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return p, 0, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, 0, err
	}
	var extra any
	if dec.Decode(&extra) != io.EOF || p.Size < 0 || p.Size > stream.MaxTransferSize {
		return p, 0, stream.ErrRequest
	}
	return p, int64(size) + 4, nil
}

// The controller bounds reads on the exact inbound connection. Body.Close alone
// cannot interrupt a server body read. The callback is joined before return.
func guestIOContext(ctx context.Context, w http.ResponseWriter, body io.ReadCloser, deadline time.Time) (func(), error) {
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(deadline); err != nil {
		return nil, err
	}
	if err := controller.SetWriteDeadline(deadline); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)
		_ = controller.SetReadDeadline(time.Now())
		_ = controller.SetWriteDeadline(time.Now())
		_ = body.Close()
	})
	return func() {
		if !stop() {
			<-done
		}
	}, nil
}

type guestHTTPInput struct{ io.Reader }

func (r guestHTTPInput) ReadContext(ctx context.Context, p []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return r.Read(p)
}

type guestHTTPOutput struct {
	http.ResponseWriter
	started bool
}

func (w *guestHTTPOutput) Write(p []byte) (int, error) {
	w.started = true
	return w.ResponseWriter.Write(p)
}

func (w *guestHTTPOutput) WriteContext(ctx context.Context, p []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return w.Write(p)
}

func (s *Server) handleGuestOperation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "method_not_allowed", "method not allowed")
		return
	}
	// Bound metadata reads before the caller deadline has been decoded.
	stop, err := guestIOContext(r.Context(), w, r.Body, time.Now().Add(30*time.Second))
	if err != nil {
		writeError(w, 400, "invalid_argument", "connection does not support guest stream deadlines")
		return
	}
	defer stop()
	p, err := readGuestHTTPRequest(r)
	if err != nil {
		writeError(w, 400, "invalid_argument", "invalid guest stream metadata or length")
		return
	}
	caller, ok := getCallerContext(r.Context())
	if !ok {
		writeError(w, 401, "unauthorized", "unauthenticated caller")
		return
	}
	p.Caller = caller
	ctx, cancel := context.WithDeadline(r.Context(), p.Deadline)
	defer cancel()
	deadlineStop, deadlineErr := guestIOContext(ctx, w, r.Body, p.Deadline)
	if deadlineErr != nil {
		return
	}
	defer deadlineStop()
	_ = http.NewResponseController(w).SetReadDeadline(p.Deadline)
	_ = http.NewResponseController(w).SetWriteDeadline(p.Deadline)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Trailer", GuestResultTrailer)
	var input io.Reader
	var output io.Writer
	streamOutput := &guestHTTPOutput{ResponseWriter: w}
	if p.Operation == "put" || p.Operation == "exec" {
		input = guestHTTPInput{r.Body}
	}
	if p.Operation == "get" || p.Operation == "exec" {
		output = streamOutput
	}
	result, opErr := s.guestService.Execute(ctx, p, input, output)
	writeGuestHTTPFinal(w, r.Header.Get(GuestErrorProtocolHeader), result, opErr, streamOutput.started)
}

func writeGuestHTTPFinal(w http.ResponseWriter, errorProtocol string, result app.GuestOperationResult, opErr error, started bool) {
	final := GuestFinal{Result: result}
	if opErr != nil {
		category := guestFailureCategory(opErr)
		if errorProtocol == GuestErrorProtocolVersion {
			final.Category = category
		} else if writeGuestEarlyError(w, category, result, started) {
			return
		}
		final.Error = "guest operation failed; inspect its category and receipt before taking another action"
		if category == "guest_workspace_unprovisioned" {
			final.Error = "guest workspace is not provisioned; an operator must install original authorized workspace identity metadata"
		}
	}
	data, err := json.Marshal(final)
	if err != nil || len(data) > GuestMetadataLimit {
		return
	}
	w.Header().Set(GuestResultTrailer, base64.RawStdEncoding.EncodeToString(data))
}
func (s *Server) handleGuestApproval(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "method_not_allowed", "method not allowed")
		return
	}
	caller, ok := getCallerContext(r.Context())
	if !ok || caller.IsDelegated() || !caller.HasScope(domain.ScopeSessionAdmin) {
		writeError(w, 403, "forbidden", "operator session:admin authority required")
		return
	}
	var req GuestApprovalRequest
	if decodeStrictJSONRequest(r, GuestMetadataLimit, &req) != nil || req.ValidForMillis < 1000 || req.ValidForMillis > 300000 {
		writeError(w, 400, "invalid_argument", "invalid guest approval metadata")
		return
	}
	req.Params.Caller = caller
	grant, receipt, err := s.guestService.IssueApprovalFor(r.Context(), req.Params, time.Duration(req.ValidForMillis)*time.Millisecond, req.Beneficiary)
	if err != nil {
		if !writeGuestEarlyError(w, guestFailureCategory(err), app.GuestOperationResult{}, false) {
			s.mapSessionError(w, err)
		}
		return
	}
	writeJSON(w, 200, GuestApprovalResponse{grant, receipt})
}

func readGuestHTTPRequest(r *http.Request) (app.GuestOperationParams, error) {
	if r.Header.Get("Content-Type") != GuestContentType || r.ContentLength < 4 {
		return app.GuestOperationParams{}, stream.ErrRequest
	}
	p, prefix, err := DecodeGuestMetadata(r.Body)
	if err != nil || r.ContentLength-prefix != p.Size || p.Deadline.IsZero() {
		return app.GuestOperationParams{}, stream.ErrRequest
	}
	return p, nil
}
