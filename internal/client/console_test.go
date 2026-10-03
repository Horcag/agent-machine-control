package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestConsoleScreenshotLargeFramePreserved(t *testing.T) {
	data := bytes.Repeat([]byte{42}, 2<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/console/screenshot" || r.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Error("incorrect request route or auth")
		}
		_ = json.NewEncoder(w).Encode(domain.ConsoleFrame{Data: data, MIMEType: "image/png"})
	}))
	defer srv.Close()
	got, err := New(srv.URL, "synthetic-token").ConsoleScreenshot(t.Context(), app.ConsoleScreenshotRequest{Width: 1024, Height: 768})
	if err != nil || !bytes.Equal(got.Data, data) {
		t.Fatalf("large screenshot bytes=%d err=%v", len(got.Data), err)
	}
}

func TestConsoleResponseLimitDoesNotBroadenGenericResponses(t *testing.T) {
	body := []byte(`{"text":"` + string(bytes.Repeat([]byte{'x'}, 2<<20)) + `"}`)
	var out struct {
		Text string `json:"text"`
	}
	if err := decodeHTTPResponse(t.Context(), bytes.NewReader(body), &out); err == nil {
		t.Fatal("generic response exceeded original bound")
	}
	frameBody, _ := json.Marshal(domain.ConsoleFrame{Data: bytes.Repeat([]byte{1}, 7<<20)})
	var frame domain.ConsoleFrame
	if err := decodeHTTPResponse(t.Context(), bytes.NewReader(frameBody), &frame); err == nil {
		t.Fatal("console response exceeded its bound")
	}
}

func TestConsoleInputCarriesApprovalAndReturnsReceipt(t *testing.T) {
	wanted := app.ConsoleInputRequest{Target: "default", Input: domain.ConsoleInput{Kind: "key", Key: "ctrl+F5"}, ApprovalID: "synthetic-approval", Deadline: "2026-10-02T10:00:00Z", Reason: "synthetic console test", IdempotencyKey: "console-client"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got app.ConsoleInputRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil || got != wanted || r.URL.Path != "/v1/console/input" || r.Method != http.MethodPost {
			t.Errorf("input contract: %+v %v", got, err)
		}
		_ = json.NewEncoder(w).Encode(domain.Receipt{ReceiptID: "synthetic-receipt", Outcome: domain.ExecutionOutcome{Status: domain.OutcomeSuccess}})
	}))
	defer srv.Close()
	got, err := New(srv.URL, "synthetic-token").ConsoleInput(t.Context(), wanted)
	if err != nil || got.ReceiptID != "synthetic-receipt" || got.Outcome.Status != domain.OutcomeSuccess {
		t.Fatalf("receipt=%+v err=%v", got, err)
	}
}

func TestConsoleInputResultSuccessKeepsFlatWireAndCacheMetadata(t *testing.T) {
	env := desktopFailureEnvelope()
	env.Receipt.OperationKind = "console.input"
	env.Receipt.Outcome = domain.ExecutionOutcome{Status: domain.OutcomeSuccess}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-AMC-Cached-Receipt", "true")
		_ = json.NewEncoder(w).Encode(env.Receipt)
	}))
	defer srv.Close()
	out, err := New(srv.URL, "synthetic-token").ConsoleInputResult(t.Context(), app.ConsoleInputRequest{})
	if err != nil || !out.CachedReceipt || !reflect.DeepEqual(out.Receipt, env.Receipt) {
		t.Fatal(out, err)
	}
}

func TestConsoleInputFailureRejectsUnsafeEvidence(t *testing.T) {
	for _, variant := range []string{"wrong operation", "class", "legacy abort", "key", "target", "cached without receipt"} {
		t.Run(variant, func(t *testing.T) {
			env := desktopFailureEnvelope()
			env.Receipt.OperationKind = "console.input"
			req := app.ConsoleInputRequest{Target: "default", IdempotencyKey: env.Receipt.IdempotencyKey}
			status := http.StatusBadRequest
			switch variant {
			case "wrong operation":
				env.Receipt.OperationKind = "desktop.action"
			case "class":
				env.Receipt.Class = domain.ClassObserve
			case "legacy abort":
				env.Receipt.EvidenceRefs = nil
				env.Receipt.Outcome = domain.ExecutionOutcome{Status: domain.OutcomeAborted, ErrorCategory: domain.FailureCategoryDeadlineExceeded, ErrorMessage: "operation deadline exceeded"}
				env.Error.Category = "timeout"
				status = http.StatusGatewayTimeout
			case "key":
				req.IdempotencyKey = "different"
			case "target":
				req.Target = "local:bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb"
			case "cached without receipt":
				env.Receipt = nil
				env.CachedReceipt = true
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(env)
			}))
			defer srv.Close()
			out, err := New(srv.URL, "synthetic-token").ConsoleInputResult(t.Context(), req)
			if !errors.Is(err, ErrMalformedResponse) || out.Receipt != nil || out.CachedReceipt {
				t.Fatal(out, err)
			}
		})
	}
}
