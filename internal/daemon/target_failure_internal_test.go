package daemon

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/target"
)

func TestStrictTargetJSONRejectsMalformedAndNestedDuplicates(t *testing.T) {
	type requestBody struct {
		Kind string `json:"kind"`
	}
	tests := []struct {
		name    string
		payload string
		limit   int64
	}{
		{name: "invalid limit", payload: `{}`, limit: 0},
		{name: "empty", payload: ``, limit: 32},
		{name: "oversized", payload: `{"kind":"enroll"}`, limit: 4},
		{name: "unknown", payload: `{"unknown":true}`, limit: 64},
		{name: "trailing", payload: `{"kind":"enroll"}{}`, limit: 64},
		{name: "nested object duplicate", payload: `{"kind":{"value":1,"value":2}}`, limit: 64},
		{name: "nested array duplicate", payload: `[{"value":1,"value":2}]`, limit: 64},
		{name: "unterminated object", payload: `{"kind":"enroll"`, limit: 64},
		{name: "unterminated array", payload: `[1,2`, limit: 64},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/target", strings.NewReader(test.payload))
			if err := decodeStrictJSONRequest(r, test.limit, &requestBody{}); err == nil {
				t.Fatal("malformed JSON unexpectedly accepted")
			}
		})
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/target", strings.NewReader(`{"items":[1,{"nested":true}],"enabled":false}`))
	var decoded map[string]any
	if err := decodeStrictJSONRequest(r, 128, &decoded); err != nil {
		t.Fatalf("valid nested JSON rejected: %v", err)
	}
}

func TestTargetHandlersRejectRequestsBeforeCoordinatorEffects(t *testing.T) {
	srv := &Server{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/target-approvals", nil)
	srv.dispatchTargetApproval(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("approval method status = %d", w.Code)
	}

	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/v1/target", nil)
	srv.handleGetTarget(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated show status = %d", w.Code)
	}

	readless, err := domain.NewActorContext("operator:readless", "operator:readless", domain.NewScopeSet(domain.ScopeTargetAdmin), domain.NewScopeSet(domain.ScopeTargetAdmin))
	if err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/v1/target", nil)
	r = r.WithContext(context.WithValue(r.Context(), callerContextKey, readless))
	srv.handleGetTarget(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("unscoped show status = %d", w.Code)
	}

	operator, err := domain.NewActorContext("operator:target", "operator:target", domain.NewScopeSet(domain.ScopeTargetAdmin), domain.NewScopeSet(domain.ScopeTargetAdmin))
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, payload, contentType string) *http.Request {
		r := httptest.NewRequest(method, "/v1/target", strings.NewReader(payload))
		r.Header.Set("Content-Type", contentType)
		return r.WithContext(context.WithValue(r.Context(), callerContextKey, operator))
	}
	w = httptest.NewRecorder()
	srv.handleIssueTargetApproval(w, request(http.MethodPost, `{}`, "text/plain"))
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("approval content type status = %d", w.Code)
	}
	w = httptest.NewRecorder()
	srv.handleIssueTargetApproval(w, request(http.MethodPost, ``, "application/json"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty approval status = %d", w.Code)
	}
	w = httptest.NewRecorder()
	srv.handleIssueTargetApproval(w, request(http.MethodPost, `{"kind":"clear","reference":"default","reason":"clear target authority","idempotency_key":"clear-with-reference","valid_for_ms":60000}`, "application/json"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("clear approval with reference status = %d", w.Code)
	}
	w = httptest.NewRecorder()
	srv.handleMutateTarget(w, request(http.MethodDelete, `{"reference":"default","reason":"clear target authority","idempotency_key":"clear-mutation-reference","approval_id":"approval-00000000000000000000000000000001","deadline":"2026-08-31T14:00:00Z"}`, "application/json"), "target.clear")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("clear mutation with reference status = %d", w.Code)
	}
}

func TestTargetMutationErrorsUseStablePublicCategories(t *testing.T) {
	srv := &Server{}
	tests := []struct {
		name   string
		err    error
		status int
	}{
		{name: "approval", err: target.ErrApprovalRequired, status: http.StatusForbidden},
		{name: "collision", err: receipt.ErrIdempotencyCollision, status: http.StatusConflict},
		{name: "reconcile", err: target.ErrMutationDrift, status: http.StatusServiceUnavailable},
		{name: "resolution", err: target.ErrNoDefault, status: http.StatusConflict},
		{name: "invalid", err: errors.New("private backend text"), status: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			srv.writeTargetMutationError(w, test.err)
			if w.Code != test.status {
				t.Fatalf("status = %d, want %d", w.Code, test.status)
			}
			if bytes.Contains(w.Body.Bytes(), []byte("private backend text")) {
				t.Fatalf("private error leaked: %s", w.Body.String())
			}
		})
	}
}
