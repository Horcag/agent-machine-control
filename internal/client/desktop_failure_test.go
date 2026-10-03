package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func desktopFailureEnvelope() daemon.DesktopErrorEnvelope {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	return daemon.DesktopErrorEnvelope{ErrorEnvelope: daemon.ErrorEnvelope{SchemaVersion: "1", Error: daemon.ErrorField{Category: "console_failed", Message: "console request failed"}}, Receipt: &domain.Receipt{
		ReceiptID: "rcpt-0123456789abcdef0123456789abcdef", OperationKind: "desktop.action", Fingerprint: domain.Fingerprint("sha256:" + strings.Repeat("a", 64)), IdempotencyFingerprint: domain.Fingerprint("sha256:" + strings.Repeat("b", 64)), IdempotencyKey: "synthetic-failure-key", Actor: "agent:mcp-local", Target: "local:c4a523d4-6b99-4d62-a5e2-4752c0f20001", Class: domain.ClassDestructivePrivileged, EffectiveBackend: "hyperv", StartedAt: now, CompletedAt: now.Add(time.Second), Outcome: domain.ExecutionOutcome{Status: domain.OutcomeFailed, ExitCode: 1}, ObservationType: domain.ObservationObserved, RedactionStatus: domain.RedactionApplied}}
}

func TestDesktopFailureEnvelopeRejectsUnsafeEvidence(t *testing.T) {
	for _, variant := range []string{"truncated", "unknown", "trailing", "duplicate", "nested duplicate", "oversized", "null", "bad schema", "bad receipt", "unredacted", "not applicable", "operation", "class", "success", "denied", "category", "clipboard contradiction", "aborted text", "target", "key", "observation", "cached without receipt", "no-receipt schema", "missing schema", "invalid signal raw message"} {
		t.Run(variant, func(t *testing.T) {
			env := desktopFailureEnvelope()
			req := app.DesktopActionRequest{Target: "default", IdempotencyKey: env.Receipt.IdempotencyKey, Request: domain.DesktopRequest{Action: "clipboard.set"}}
			status := http.StatusBadRequest
			variants := map[string]func(){
				"bad schema":     func() { env.SchemaVersion = "2" },
				"bad receipt":    func() { env.Receipt.ReceiptID = "bad" },
				"unredacted":     func() { env.Receipt.RedactionStatus = domain.RedactionFailed },
				"not applicable": func() { env.Receipt.RedactionStatus = domain.RedactionNotApplicable },
				"operation":      func() { env.Receipt.OperationKind = "console.input" },
				"class":          func() { env.Receipt.Class = domain.ClassObserve },
				"success":        func() { env.Receipt.Outcome.Status = domain.OutcomeSuccess },
				"denied":         func() { env.Receipt.Outcome.Status = domain.OutcomeDenied },
				"category": func() {
					env.Receipt.Outcome.ErrorCategory = "raw"
					env.Receipt.Outcome.ErrorMessage = "synthetic-private-error"
				},
				"clipboard contradiction": func() {
					env.Error.Category = domain.FailureCategoryClipboardUncertain
					status = http.StatusConflict
				},
				"aborted text": func() {
					env.Error.Category = "timeout"
					status = http.StatusGatewayTimeout
					env.Receipt.Outcome = domain.ExecutionOutcome{Status: domain.OutcomeAborted, ErrorCategory: domain.FailureCategoryDeadlineExceeded, ErrorMessage: "synthetic-private-error"}
				},
				"target":      func() { req.Target = "local:bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb" },
				"key":         func() { req.IdempotencyKey = "other-key" },
				"observation": func() { req.Request.Action = "windows" },
				"invalid signal raw message": func() {
					env.Receipt = nil
					env.ReceiptInvalid = true
					env.Error.Message = "synthetic-private-provider-output"
				},
				"no-receipt schema": func() {
					env.Receipt = nil
					env.SchemaVersion = "2"
				},
				"missing schema": func() {
					env.Receipt = nil
					env.SchemaVersion = ""
				},
				"cached without receipt": func() {
					env.Receipt = nil
					env.CachedReceipt = true
				},
			}
			if modify := variants[variant]; modify != nil {
				modify()
			}
			data, err := json.Marshal(env)
			if err != nil {
				t.Fatal(err)
			}
			switch variant {
			case "truncated":
				data = data[:len(data)-1]
			case "unknown":
				data = append([]byte(`{"response":{"text":"synthetic-private-response"},`), data[1:]...)
			case "trailing":
				data = append(data, []byte(` {}`)...)
			case "duplicate":
				data = append([]byte(`{"receipt":null,`), data[1:]...)
			case "nested duplicate":
				data = []byte(strings.Replace(string(data), `"Status":"failed"`, `"Status":"success","Status":"failed"`, 1))
			case "oversized":
				data = append(data, []byte(strings.Repeat(" ", 64*1024))...)
			case "null":
				data = []byte(`null`)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status); _, _ = w.Write(data) }))
			defer srv.Close()
			out, err := New(srv.URL, "synthetic-token").DesktopAction(t.Context(), req)
			if !errors.Is(err, ErrMalformedResponse) || !reflect.DeepEqual(out, app.DesktopActionResult{}) {
				t.Fatalf("unsafe %s: %+v %v", variant, out, err)
			}
			if strings.Contains(err.Error(), "synthetic-private") {
				t.Fatal("raw parser text escaped")
			}
		})
	}
}

func TestDesktopFailureEnvelopePreservesCanonicalErrors(t *testing.T) {
	for _, cause := range []string{"generic", "clipboard", "deadline", "cancelled"} {
		for _, cached := range []bool{false, true} {
			t.Run(cause+"/"+map[bool]string{false: "fresh", true: "cached"}[cached], func(t *testing.T) {
				env := desktopFailureEnvelope()
				env.CachedReceipt = cached
				status := http.StatusBadRequest
				want := ErrInvalidArgument
				switch cause {
				case "clipboard":
					status = http.StatusConflict
					want = domain.ErrClipboardUncertain
					env.Error.Category = domain.FailureCategoryClipboardUncertain
					env.Receipt.Outcome.ErrorCategory = domain.FailureCategoryClipboardUncertain
					env.Receipt.Outcome.ErrorMessage, _ = domain.CanonicalFailureMessage(env.Error.Category)
				case "deadline":
					status = http.StatusGatewayTimeout
					want = ErrTimeout
					env.Error.Category = "timeout"
					env.Receipt.Outcome = domain.ExecutionOutcome{Status: domain.OutcomeAborted, ErrorCategory: domain.FailureCategoryDeadlineExceeded, ErrorMessage: "operation deadline exceeded"}
				case "cancelled":
					status = http.StatusRequestTimeout
					want = nil
					env.Error.Category = "cancelled"
					env.Receipt.Outcome = domain.ExecutionOutcome{Status: domain.OutcomeAborted, ErrorCategory: domain.FailureCategoryCallerCanceled, ErrorMessage: "operation cancelled"}
				}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(status)
					_ = json.NewEncoder(w).Encode(env)
				}))
				defer srv.Close()
				out, err := New(srv.URL, "synthetic-token").DesktopAction(context.Background(), app.DesktopActionRequest{Target: "primary", IdempotencyKey: env.Receipt.IdempotencyKey, Request: domain.DesktopRequest{Action: "clipboard.set"}})
				if err == nil || (want != nil && !errors.Is(err, want)) || !reflect.DeepEqual(out.Receipt, env.Receipt) || out.CachedReceipt != cached {
					t.Fatalf("lost receipt/error: %+v %v", out, err)
				}
				api, ok := errors.AsType[*APIError](err)
				if !ok || api.StatusCode != status || api.Category != env.Error.Category {
					t.Fatal("canonical error changed")
				}
			})
		}
	}
}

func TestDesktopInvalidReceiptSignalPreservesClipboardUncertainty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"schema_version":"1","error":{"category":"clipboard_possibly_cleared","message":"clipboard may have been cleared"},"cached_receipt":false,"receipt_invalid":true}`))
	}))
	defer srv.Close()
	out, err := New(srv.URL, "synthetic-token").DesktopAction(t.Context(), app.DesktopActionRequest{})
	if !errors.Is(err, ErrMalformedResponse) || !errors.Is(err, domain.ErrClipboardUncertain) || out.Receipt != nil || !strings.Contains(err.Error(), "effects are unknown") {
		t.Fatal(out, err)
	}
}
