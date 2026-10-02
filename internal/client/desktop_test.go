package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func assertDesktopClientRequest(t *testing.T, r *http.Request, path string, expected any) {
	t.Helper()
	if r.Method != http.MethodPost || r.URL.Path != path || r.Header.Get("Authorization") != "Bearer synthetic-agent-token" {
		t.Errorf("request %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
	}
	var got, want any
	if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
		t.Error(err)
	}
	encoded, err := json.Marshal(expected)
	if err != nil {
		t.Error(err)
	}
	if err := json.Unmarshal(encoded, &want); err != nil {
		t.Error(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("request got=%v want=%v", got, want)
	}
}

func TestDesktopClientAuthenticatedContracts(t *testing.T) {
	action := app.DesktopActionRequest{Target: "default", Request: domain.DesktopRequest{Action: "clipboard.set", Text: "synthetic text", RequestID: "0123456789abcdef0123456789abcdef", Deadline: "2026-10-02T20:00:00Z"}, Reason: "test action", IdempotencyKey: "action-key", LabGrantID: "own-grant"}
	issue := app.ConsoleLabGrantIssueRequest{Target: "default", Beneficiary: "agent:mcp-local", Reason: "test grant", IdempotencyKey: "issue-key", ValidForMillis: 60000, AcknowledgeExternalEffects: true}
	revoke := app.ConsoleLabGrantRevokeRequest{GrantID: "own-grant", Reason: "test revoke", IdempotencyKey: "revoke-key", Deadline: action.Request.Deadline}
	record := app.ConsoleRecordRequest{Target: "default", Width: 8, Height: 4, Frames: 2, IntervalMillis: 100}
	for _, tc := range []struct {
		name, path        string
		request, response any
		call              func(*Client) (any, error)
	}{
		{"action", "/v1/desktop/action", action, app.DesktopActionResult{Response: domain.DesktopResponse{Success: true, RequestID: action.Request.RequestID}}, func(c *Client) (any, error) { return c.DesktopAction(t.Context(), action) }},
		{"issue", "/v1/desktop/lab/issue", issue, ConsoleLabGrantResult{Grant: app.ConsoleLabGrant{GrantID: "own-grant"}, Receipt: domain.Receipt{ReceiptID: "issue-receipt"}}, func(c *Client) (any, error) { return c.IssueConsoleLabGrant(t.Context(), issue) }},
		{"status", "/v1/desktop/lab/status", map[string]string{"grant_id": "own-grant"}, app.ConsoleLabGrantStatus{State: "active", Grant: app.ConsoleLabGrant{GrantID: "own-grant"}}, func(c *Client) (any, error) { return c.ConsoleLabGrantStatus(t.Context(), "own-grant") }},
		{"active", "/v1/desktop/lab/active", map[string]string{"target": "default"}, app.ConsoleLabGrantStatus{State: "active", Grant: app.ConsoleLabGrant{GrantID: "own-grant"}}, func(c *Client) (any, error) { return c.ActiveConsoleLabGrant(t.Context(), "default") }},
		{"revoke", "/v1/desktop/lab/revoke", revoke, domain.Receipt{ReceiptID: "revoke-receipt"}, func(c *Client) (any, error) { return c.RevokeConsoleLabGrant(t.Context(), revoke) }},
		{"record", "/v1/console/record", record, app.ConsoleRecording{Data: []byte("GIF89a synthetic"), MIMEType: "image/gif", SHA256: "synthetic-digest", Width: 8, Height: 4}, func(c *Client) (any, error) { return c.ConsoleRecord(t.Context(), record) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				assertDesktopClientRequest(t, r, tc.path, tc.request)
				if err := json.NewEncoder(w).Encode(tc.response); err != nil {
					t.Error(err)
				}
			}))
			defer srv.Close()
			got, err := tc.call(New(srv.URL, "synthetic-agent-token"))
			if err != nil || calls != 1 || !reflect.DeepEqual(got, tc.response) {
				t.Fatalf("response=%+v calls=%d err=%v", got, calls, err)
			}
		})
	}
}
