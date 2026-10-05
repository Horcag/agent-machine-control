package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestListReceiptsAuthenticationLimitsAndDTO(t *testing.T) {
	for _, test := range []struct{ input, want int }{{0, 50}, {-1, 50}, {7, 7}, {1000, 1000}, {1001, 1000}} {
		t.Run(strconv.Itoa(test.input), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v1/receipts" || r.URL.Query().Get("limit") != strconv.Itoa(test.want) || r.Header.Get("Authorization") != "Bearer synthetic-token" {
					t.Errorf("unexpected request: %s %s auth=%q", r.Method, r.URL, r.Header.Get("Authorization"))
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": "1", "receipts": []map[string]any{{"receipt_id": "synthetic-receipt", "idempotency_key": "exact-key", "actor": "agent:mcp-local", "outcome": map[string]any{"status": "aborted", "error_category": "caller_canceled"}}}})
			}))
			defer server.Close()
			got, err := New(server.URL, "synthetic-token").ListReceipts(t.Context(), test.input)
			if err != nil || len(got) != 1 || got[0].IdempotencyKey != "exact-key" || got[0].Outcome.ErrorCategory != "caller_canceled" {
				t.Fatalf("receipts=%+v err=%v", got, err)
			}
		})
	}
}

func TestListReceiptsErrorsAndCanceledContext(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{{"unauthorized", 401, `{"error":{"category":"unauthorized","message":"unauthorized"}}`}, {"server", 500, `{"error":{"category":"internal_error","message":"failed"}}`}, {"malformed", 200, `{`}} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			if got, err := New(server.URL, "synthetic-token").ListReceipts(t.Context(), 1); err == nil || got != nil {
				t.Fatalf("receipts=%+v err=%v", got, err)
			}
		})
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := New(server.URL, "synthetic-token").ListReceipts(ctx, 1); err == nil || got != nil || calls != 0 {
		t.Fatalf("receipts=%+v err=%v calls=%d", got, err, calls)
	}
}
