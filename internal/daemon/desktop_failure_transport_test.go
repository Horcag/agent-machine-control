package daemon_test

import (
	"context"
	"encoding/json"
	"errors"
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

func failureRequest() app.DesktopActionRequest {
	return app.DesktopActionRequest{Target: "default", Reason: "synthetic desktop failure", IdempotencyKey: "synthetic-failed-action", Request: domain.DesktopRequest{Action: "clipboard.set", Text: "synthetic-private-input", RequestID: strings.Repeat("a", 32), Deadline: time.Now().Add(45 * time.Second).UTC().Format(time.RFC3339Nano)}}
}

func TestDesktopDaemonClientNormalFailureReceipt(t *testing.T) {
	endpoint, operator, agent, _, calls, approve := daemon.DesktopFailureTestServer(t, errors.New("synthetic-private-provider-error"))
	req := approve(failureRequest())
	cl := client.New(endpoint, agent)
	first, err := cl.DesktopAction(t.Context(), req)
	assertTransportFailure(t, first, err, false)
	retry, err := cl.DesktopAction(t.Context(), req)
	assertTransportFailure(t, retry, err, true)
	if !reflect.DeepEqual(first.Receipt, retry.Receipt) || calls() != 1 {
		t.Fatal("failed retry changed receipt or redispatched")
	}
	for _, variant := range []string{"actor", "target", "payload", "approval", "expired", "unauthenticated"} {
		changed, token := req, agent
		switch variant {
		case "actor":
			token = operator
		case "target":
			changed.Target = "bbbbbbbb-bbbb-4bbb-bbbb-bbbbbbbbbbbb"
		case "payload":
			changed.Request.Text = "changed synthetic input"
		case "approval":
			changed.ApprovalID = app.ConsoleLabApprovalPrefix + strings.Repeat("b", 32)
		case "expired":
			changed.Request.Deadline = time.Now().Add(-time.Minute).Format(time.RFC3339Nano)
		case "unauthenticated":
			token = ""
		}
		out, err := client.New(endpoint, token).DesktopAction(t.Context(), changed)
		if err == nil || out.Receipt != nil || out.CachedReceipt || calls() != 1 {
			t.Fatalf("%s returned evidence or dispatched", variant)
		}
	}
}

func assertTransportFailure(t *testing.T, out app.DesktopActionResult, err error, cached bool) {
	t.Helper()
	if err == nil || out.Receipt == nil || out.Receipt.Validate() != nil || out.CachedReceipt != cached || out.Receipt.Actor != "agent:mcp-local" || out.Receipt.OperationKind != "desktop.action" || !reflect.DeepEqual(out.Response, domain.DesktopResponse{}) {
		t.Fatalf("failure result: %+v %v", out, err)
	}
	data, _ := json.Marshal(out)
	if strings.Contains(string(data), "synthetic-private") || strings.Contains(err.Error(), "synthetic-private") {
		t.Fatal("private payload escaped")
	}
}

func TestDesktopDaemonClientMCPFailureReceipt(t *testing.T) {
	for _, cause := range []error{errors.New("synthetic-private-provider-error"), context.DeadlineExceeded, context.Canceled, domain.ErrClipboardUncertain} {
		t.Run(cause.Error(), func(t *testing.T) {
			testDesktopMCPFailureReceipt(t, cause)
		})
	}
}

func testDesktopMCPFailureReceipt(t *testing.T, cause error) {
	t.Helper()
	endpoint, operator, _, root, calls, _ := daemon.DesktopFailureTestServer(t, cause)
	grant, err := client.New(endpoint, operator).IssueConsoleLabGrant(t.Context(), app.ConsoleLabGrantIssueRequest{Target: "default", Reason: "synthetic lab failure", IdempotencyKey: "synthetic-lab-grant", Beneficiary: "agent:mcp-local", ValidForMillis: 60000})
	if err != nil {
		t.Fatal(err)
	}
	a := mcpadapter.NewAdapter(root)
	ct, st := mcp.NewInMemoryTransports()
	ss, err := a.BuildServer().Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "synthetic-failure-test", Version: "1"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	req := failureRequest()
	in := mcpadapter.DesktopActInput{Target: req.Target, Action: &req.Request, LabGrantID: grant.Grant.GrantID, Reason: req.Reason, IdempotencyKey: req.IdempotencyKey, Deadline: req.Request.Deadline, ObserveAfter: true}
	var first *domain.Receipt
	for attempt := range 2 {
		result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "desktop_act", Arguments: in})
		if err != nil || result == nil || !result.IsError {
			t.Fatalf("MCP: %+v %v", result, err)
		}
		first = assertMCPDesktopFailure(t, result, attempt, first)
	}
	if calls() != 1 {
		t.Fatal("MCP retry redispatched")
	}
	shown, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "receipt_show", Arguments: map[string]string{"receipt_id": string(first.ReceiptID)}})
	if err != nil || shown.IsError {
		t.Fatalf("receipt_show: %+v %v", shown, err)
	}
}

func assertMCPDesktopFailure(t *testing.T, result *mcp.CallToolResult, attempt int, first *domain.Receipt) *domain.Receipt {
	t.Helper()
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out mcpadapter.DesktopActResult
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	assertTransportFailure(t, out.Result, errors.New("synthetic execution failed"), attempt == 1)
	if out.Observation != nil {
		t.Fatal("failure performed post-observation")
	}
	if attempt == 0 {
		first = out.Result.Receipt
	} else if !reflect.DeepEqual(first, out.Result.Receipt) {
		t.Fatal("cached receipt changed")
	}
	all, _ := json.Marshal(result)
	if strings.Contains(string(all), "synthetic-private") || !strings.Contains(string(all), "receipt_id: "+string(first.ReceiptID)) {
		t.Fatalf("MCP receipt reference/redaction: %s", all)
	}
	return first
}
