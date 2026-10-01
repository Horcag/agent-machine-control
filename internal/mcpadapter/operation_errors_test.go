package mcpadapter

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMutationTerminalFailurePreservesRecoveryReferences(t *testing.T) {
	const operationID = "op-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const receiptID = "rcpt-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const machineID = "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := map[string]any{"schema_version": "1", "operation_id": operationID, "state": "admitted"}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/operations":
		case r.Method == http.MethodGet && r.URL.Path == "/v1/operations/"+operationID:
			payload["state"] = "failed"
			payload["error_category"] = "timeout"
			payload["error_message"] = "synthetic credential=secret-value /synthetic/private/key"
			payload["receipt_id"] = receiptID
		default:
			t.Errorf("unexpected recovery/follow-up request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	cases := []struct {
		name string
		call func(*Adapter) (*mcp.CallToolResult, error)
	}{
		{"start", func(a *Adapter) (*mcp.CallToolResult, error) {
			result, _, err := a.MachineStart(t.Context(), nil, MachineStartInput{ID: machineID, Reason: "test", IdempotencyKey: "failure-start", Timeout: "30s"})
			return result, err
		}},
		{"stop", func(a *Adapter) (*mcp.CallToolResult, error) {
			result, _, err := a.MachineStop(t.Context(), nil, MachineStopInput{ID: machineID, Mode: "shutdown", Reason: "test", IdempotencyKey: "failure-stop", Timeout: "30s"})
			return result, err
		}},
		{"checkpoint create", func(a *Adapter) (*mcp.CallToolResult, error) {
			result, _, err := a.CheckpointCreate(t.Context(), nil, CheckpointCreateInput{ID: machineID, Name: "synthetic-point", Reason: "test", IdempotencyKey: "failure-checkpoint", Timeout: "30s"})
			return result, err
		}},
		{"checkpoint restore", func(a *Adapter) (*mcp.CallToolResult, error) {
			result, _, err := a.CheckpointRestore(t.Context(), nil, CheckpointRestoreInput{ID: machineID, CheckpointID: "c4a523d4-6b99-4d62-a5e2-4752c0f20002", Reason: "test", IdempotencyKey: "failure-restore", Timeout: "30s"})
			return result, err
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			adapter := &Adapter{client: client.New(server.URL, "synthetic-token"), allowUnscopedTestTargetFallback: true}
			result, err := test.call(adapter)
			if err != nil || result == nil || !result.IsError || len(result.Content) != 2 {
				t.Fatalf("terminal failure result=%v error=%v", result, err)
			}
			if got := result.Content[0].(*mcp.TextContent).Text; got != "operation_timeout: operation timeout exceeded" {
				t.Fatalf("safe category = %q", got)
			}
			if got := result.Content[1].(*mcp.TextContent).Text; got != "operation_id: "+operationID+"; receipt_id: "+receiptID {
				t.Fatalf("recovery references = %q", got)
			}
		})
	}
}

func TestOperationErrorRejectsUntrustedReferencesAndCategories(t *testing.T) {
	cases := []struct {
		category, operationID, receiptID, message, reference string
	}{
		{"unknown-secret-value", "op-secret-value", "rcpt-secret-value", "operation_failed: operation failed; inspect its receipt for details", ""},
		{"cancelled", "op-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "/synthetic/private/key", "operation_canceled: operation was canceled", "operation_id: op-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"approval_required", "", "rcpt-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "approval_required: operator approval required for this operation", "receipt_id: rcpt-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}
	for _, test := range cases {
		result := operationToolError(&operationFailure{category: test.category}, test.operationID, test.receiptID)
		if got := result.Content[0].(*mcp.TextContent).Text; got != test.message {
			t.Fatalf("failure text = %q", got)
		}
		if test.reference == "" {
			if len(result.Content) != 1 {
				t.Fatalf("invalid reference was included: %v", result.Content)
			}
		} else if len(result.Content) != 2 || result.Content[1].(*mcp.TextContent).Text != test.reference {
			t.Fatalf("references = %v", result.Content)
		}
	}
}

func TestOperationWaitErrorRetainsAdmittedReference(t *testing.T) {
	const operationID = "op-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/operations" {
			_, _ = w.Write([]byte(`{"schema_version":"1","operation_id":"` + operationID + `","state":"admitted"}`))
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/v1/operations/"+operationID {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusGatewayTimeout)
		_, _ = w.Write([]byte(`{"error":{"category":"timeout","message":"secret-value"}}`))
	}))
	defer server.Close()
	adapter := &Adapter{client: client.New(server.URL, "synthetic-token"), allowUnscopedTestTargetFallback: true}
	result, _, err := adapter.MachineStart(t.Context(), nil, MachineStartInput{
		ID: "c4a523d4-6b99-4d62-a5e2-4752c0f20001", Reason: "test", IdempotencyKey: "wait-failure", Timeout: "30s",
	})
	if err != nil || result == nil || !result.IsError || len(result.Content) != 2 {
		t.Fatalf("wait failure result=%v error=%v", result, err)
	}
	if result.Content[0].(*mcp.TextContent).Text != "operation_timeout: operation timeout exceeded" || result.Content[1].(*mcp.TextContent).Text != "operation_id: "+operationID {
		t.Fatalf("wait failure = %v", result.Content)
	}
}

func TestAPICategoryErrorsAreSafeAndDistinct(t *testing.T) {
	cases := []struct{ category, want string }{
		{"host_unavailable", "backend_host_unavailable: backend management host is unavailable"},
		{"module_missing", "backend_module_missing: Hyper-V PowerShell module is unavailable"},
		{"access_denied", "backend_access_denied: machine backend access denied"},
		{"malformed_response", "backend_malformed_response: machine backend returned an invalid response"},
		{"timeout", "operation_timeout: operation timeout exceeded"},
		{"cancelled", "operation_canceled: operation was canceled"},
		{"backend_error", "backend_error: backend operation failed"},
		{"secret-value", "daemon_malformed_response: daemon returned an invalid response"},
	}
	for _, test := range cases {
		t.Run(test.category, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				if err := json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"category": test.category, "message": "secret-value /synthetic/private/key"}}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			_, err := client.New(server.URL, "synthetic-token").GetOperation(t.Context(), "op-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
			if err == nil {
				t.Fatal("expected API error")
			}
			result := mcpToolError(err)
			if got := result.Content[0].(*mcp.TextContent).Text; !result.IsError || got != test.want {
				t.Fatalf("API tool error = %q, want %q", got, test.want)
			}
		})
	}
}
