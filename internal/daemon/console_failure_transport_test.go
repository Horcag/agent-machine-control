package daemon_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/mcpadapter"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestNativeConsoleDaemonClientFailureReceipt(t *testing.T) {
	for _, cause := range []error{errors.New("synthetic-private-provider"), context.DeadlineExceeded, context.Canceled} {
		t.Run(cause.Error(), func(t *testing.T) {
			endpoint, operator, agent, _, calls, _ := daemon.DesktopFailureTestServer(t, cause)
			cl := client.New(endpoint, agent)
			req := app.ConsoleInputRequest{Target: "default", Input: domain.ConsoleInput{Kind: "type", Text: "synthetic-private-input"}, Reason: "synthetic native failure", IdempotencyKey: "synthetic-native-failure"}
			grant, err := client.New(endpoint, operator).IssueOperationApproval(t.Context(), daemon.OperationApprovalIssueRequest{Kind: "console.input", Target: req.Target, Reason: req.Reason, IdempotencyKey: req.IdempotencyKey, ValidForMillis: 45000, Beneficiary: "agent:mcp-local", Parameters: domain.ConsoleInputParameters(req.Input)})
			if err != nil {
				t.Fatal(err)
			}
			req.ApprovalID, req.Deadline = grant.ApprovalID, grant.Deadline
			first, err := cl.ConsoleInputResult(t.Context(), req)
			assertNativeFailure(t, first, err, false)
			retry, err := cl.ConsoleInputResult(t.Context(), req)
			assertNativeFailure(t, retry, err, true)
			if !reflect.DeepEqual(first.Receipt, retry.Receipt) || calls() != 1 {
				t.Fatal("native retry redispatched")
			}
			legacy, err := cl.ConsoleInput(t.Context(), req)
			if err == nil || !reflect.DeepEqual(first.Receipt, &legacy) || calls() != 1 {
				t.Fatal("receipt-only client lost evidence")
			}
		})
	}
}

func assertNativeFailure(t *testing.T, out app.ConsoleInputResult, err error, cached bool) {
	t.Helper()
	if err == nil || out.Receipt == nil || out.Receipt.Validate() != nil || out.CachedReceipt != cached || out.Receipt.OperationKind != "console.input" || out.Receipt.Class != domain.ClassDestructivePrivileged {
		t.Fatal(out, err)
	}
	data, _ := json.Marshal(out)
	if strings.Contains(string(data), "synthetic-private") || strings.Contains(err.Error(), "synthetic-private") {
		t.Fatal("native private payload escaped")
	}
}

func TestNativeDesktopMCPFailureReceiptAndRejection(t *testing.T) {
	for _, cause := range []error{errors.New("synthetic-private-provider"), context.DeadlineExceeded, context.Canceled} {
		t.Run(cause.Error(), func(t *testing.T) {
			testNativeDesktopMCP(t, cause)
		})
	}
}

func testNativeDesktopMCP(t *testing.T, cause error) {
	t.Helper()
	endpoint, operator, agent, root, calls, _ := daemon.DesktopFailureTestServer(t, cause)
	cl := client.New(endpoint, operator)
	grant, err := cl.IssueConsoleLabGrant(t.Context(), app.ConsoleLabGrantIssueRequest{Target: "default", Reason: "synthetic native lab", IdempotencyKey: "synthetic-native-lab", Beneficiary: "agent:mcp-local", ValidForMillis: 60000})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := client.New(endpoint, agent).ConsoleScreenshot(t.Context(), app.ConsoleScreenshotRequest{Target: "default", Width: 100, Height: 50})
	if err != nil {
		t.Fatal(err)
	}
	ct, st := mcp.NewInMemoryTransports()
	ss, err := mcpadapter.NewAdapter(root).BuildServer().Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "synthetic-native", Version: "1"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	input := domain.ConsoleInput{Kind: "drag", FrameID: frame.FrameID, Button: "left", X: 1, Y: 1, ToX: 20, ToY: 20}
	in := mcpadapter.DesktopActInput{Target: "default", Input: &input, LabGrantID: grant.Grant.GrantID, Reason: "synthetic mid-drag failure", IdempotencyKey: "synthetic-native-drag", Deadline: time.Now().Add(45 * time.Second).UTC().Format(time.RFC3339Nano), ObserveAfter: true}
	first := assertNativeMCPRetries(t, cs, in)
	if calls() != 1 {
		t.Fatal("native MCP retry redispatched")
	}
	shown, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "receipt_show", Arguments: map[string]string{"receipt_id": string(first.ReceiptID)}})
	if err != nil || shown.IsError {
		t.Fatal(shown, err)
	}
	// Invalid frames must not create zero receipts or poison a future retry.
	in.IdempotencyKey = "synthetic-invalid-frame"
	original := frame
	original.Data = nil
	frame.Data = nil
	frame.VMID = "local:bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb"
	path := filepath.Join(root, "console-frames", frame.FrameID)
	data, _ := json.Marshal(frame)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	assertNativeMCPNoReceipt(t, cs, in)
	data, _ = json.Marshal(original)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "desktop_act", Arguments: in})
	if err != nil || !result.IsError || calls() != 2 {
		t.Fatal("frame failure poisoned retry", result, err)
	}
	if _, err := cl.RevokeConsoleLabGrant(t.Context(), app.ConsoleLabGrantRevokeRequest{GrantID: grant.Grant.GrantID, Reason: "synthetic revoke", IdempotencyKey: "synthetic-native-revoke", Deadline: in.Deadline}); err != nil {
		t.Fatal(err)
	}
	in.IdempotencyKey = "synthetic-native-drag"
	assertNativeMCPNoReceipt(t, cs, in)
	if calls() != 2 {
		t.Fatal("revoked grant dispatched")
	}
}

func assertNativeMCPRetries(t *testing.T, cs *mcp.ClientSession, in mcpadapter.DesktopActInput) *domain.Receipt {
	t.Helper()
	var first *domain.Receipt
	for attempt := range 2 {
		result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "desktop_act", Arguments: in})
		if err != nil || result == nil || !result.IsError {
			t.Fatal(result, err)
		}
		data, _ := json.Marshal(result.StructuredContent)
		var out mcpadapter.DesktopActResult
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatal(err)
		}
		assertNativeFailure(t, app.ConsoleInputResult{Receipt: out.Result.Receipt, CachedReceipt: out.Result.CachedReceipt}, errors.New("synthetic failure"), attempt == 1)
		if out.Observation != nil || !reflect.DeepEqual(out.Result.Response, domain.DesktopResponse{}) {
			t.Fatal("partial response or observation escaped")
		}
		if attempt == 0 {
			first = out.Result.Receipt
		} else if !reflect.DeepEqual(first, out.Result.Receipt) {
			t.Fatal("native MCP receipt changed")
		}
		all, _ := json.Marshal(result)
		if strings.Contains(string(all), "synthetic-private") || strings.Contains(string(all), "image/png") || !strings.Contains(string(all), "receipt_id: "+string(first.ReceiptID)) {
			t.Fatal("native MCP privacy/reference", string(all))
		}
	}
	return first
}

func assertNativeMCPNoReceipt(t *testing.T, cs *mcp.ClientSession, in mcpadapter.DesktopActInput) {
	t.Helper()
	result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "desktop_act", Arguments: in})
	if err != nil || result == nil || !result.IsError {
		t.Fatal(result, err)
	}
	data, _ := json.Marshal(result.StructuredContent)
	var out mcpadapter.DesktopActResult
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Result.Receipt != nil || out.Result.CachedReceipt || out.Observation != nil {
		t.Fatal("rejected input carried evidence", out)
	}
}
