package mcpadapter

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/backends/hyperv"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func callMutationForObservationError(t *testing.T, adapter *Adapter, kind string) (*mcp.CallToolResult, error) {
	t.Helper()
	const machineID = "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	var result *mcp.CallToolResult
	var err error
	switch kind {
	case "machine.start":
		result, _, err = adapter.MachineStart(t.Context(), nil, MachineStartInput{ID: machineID, Reason: "test", IdempotencyKey: "completed-start", Timeout: "30s"})
	case "machine.stop":
		result, _, err = adapter.MachineStop(t.Context(), nil, MachineStopInput{ID: machineID, Mode: "shutdown", Reason: "test", IdempotencyKey: "completed-stop", Timeout: "30s"})
	case "checkpoint.create":
		result, _, err = adapter.CheckpointCreate(t.Context(), nil, CheckpointCreateInput{ID: machineID, Name: "synthetic-point", Reason: "test", IdempotencyKey: "completed-checkpoint", Timeout: "30s"})
	case "checkpoint.restore":
		result, _, err = adapter.CheckpointRestore(t.Context(), nil, CheckpointRestoreInput{ID: machineID, CheckpointID: "c4a523d4-6b99-4d62-a5e2-4752c0f20002", Reason: "test", IdempotencyKey: "completed-restore", Timeout: "30s"})
	default:
		t.Fatalf("unknown fixture mutation %q", kind)
	}
	return result, err
}

func TestCompletedMutationObservationFailurePreservesReceipt(t *testing.T) {
	const operationID = "op-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const receiptID = "rcpt-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	cases := []struct {
		kind    string
		cause   error
		message string
	}{
		{"machine.start", hyperv.ErrHostUnavailable, "backend_host_unavailable: backend management host is unavailable"},
		{"machine.stop", hyperv.ErrHostUnavailable, "backend_host_unavailable: backend management host is unavailable"},
		{"checkpoint.create", hyperv.ErrHostUnavailable, "backend_host_unavailable: backend management host is unavailable"},
		{"checkpoint.restore", hyperv.ErrHostUnavailable, "backend_host_unavailable: backend management host is unavailable"},
		{"machine.start", context.Canceled, "operation_canceled: operation was canceled"},
		{"machine.stop", context.Canceled, "operation_canceled: operation was canceled"},
		{"checkpoint.create", context.Canceled, "operation_canceled: operation was canceled"},
		{"checkpoint.restore", context.Canceled, "operation_canceled: operation was canceled"},
	}
	for _, test := range cases {
		t.Run(test.kind+"/"+test.cause.Error(), func(t *testing.T) {
			adapter, backend := mcpExactTargetHarness(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/operations":
					_, _ = w.Write([]byte(`{"schema_version":"1","operation_id":"` + operationID + `","state":"admitted"}`))
				case "/v1/operations/" + operationID:
					_, _ = w.Write([]byte(`{"schema_version":"1","operation_id":"` + operationID + `","state":"completed","receipt_id":"` + receiptID + `"}`))
				case "/v1/receipts/" + receiptID:
					backend.inspectErr = fmt.Errorf("synthetic secret-value: %w", test.cause)
					_, _ = w.Write([]byte(`{"schema_version":"1","receipt":{"receipt_id":"` + receiptID + `","operation_kind":"` + test.kind + `","fingerprint":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","actor":"agent:mcp-local","target":"local:c4a523d4-6b99-4d62-a5e2-4752c0f20001","class":"reversible_mutation","effective_backend":"hyperv","started_at":"2026-08-31T01:59:00Z","completed_at":"2026-08-31T01:59:01Z","outcome":{"status":"success","exit_code":0},"observation_type":"observed","redaction_status":"applied"}}`))
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			adapter.client = client.New(server.URL, "synthetic-token")
			result, err := callMutationForObservationError(t, adapter, test.kind)
			if err != nil || result == nil || !result.IsError || len(result.Content) != 2 {
				t.Fatalf("post-mutation failure result=%v error=%v", result, err)
			}
			if message := result.Content[0].(*mcp.TextContent).Text; message != test.message {
				t.Fatalf("observation failure=%q", message)
			}
			if reference := result.Content[1].(*mcp.TextContent).Text; reference != "operation_id: "+operationID+"; receipt_id: "+receiptID {
				t.Fatalf("completed mutation recovery=%q", reference)
			}
		})
	}
}
