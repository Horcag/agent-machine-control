package mcpadapter

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func desktopOutcomeReceipt(failed bool) *domain.Receipt {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	outcome := domain.ExecutionOutcome{Status: domain.OutcomeSuccess}
	if failed {
		outcome = domain.ExecutionOutcome{Status: domain.OutcomeFailed, ExitCode: 1}
	}
	return &domain.Receipt{
		ReceiptID: "rcpt-0123456789abcdef0123456789abcdef", OperationKind: "console.input",
		Fingerprint:            domain.Fingerprint("sha256:" + strings.Repeat("a", 64)),
		IdempotencyFingerprint: domain.Fingerprint("sha256:" + strings.Repeat("b", 64)),
		IdempotencyKey:         "synthetic-outcome-key", Actor: "agent:mcp-local", Target: domain.MachineRef(desktopVMA),
		Class: domain.ClassDestructivePrivileged, EffectiveBackend: "hyperv", StartedAt: now, CompletedAt: now.Add(time.Second),
		Outcome: outcome, ObservationType: domain.ObservationObserved, RedactionStatus: domain.RedactionApplied,
		EvidenceRefs: []string{domain.DesktopDispatchEvidence},
	}
}

func desktopOutcomeJSON(t *testing.T, value any) map[string]json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var outer struct {
		SchemaVersion string                     `json:"schema_version"`
		Result        map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &outer); err != nil || outer.SchemaVersion != SchemaVersion || outer.Result == nil {
		t.Fatalf("desktop outcome=%s err=%v", data, err)
	}
	return outer.Result
}

func assertNativeOutcome(t *testing.T, value any, receipt *domain.Receipt, cached bool) {
	t.Helper()
	outcome := desktopOutcomeJSON(t, value)
	if _, exists := outcome["response"]; exists {
		t.Fatalf("native input fabricated a guest response: %s", outcome["response"])
	}
	var got domain.Receipt
	if err := json.Unmarshal(outcome["receipt"], &got); err != nil || !reflect.DeepEqual(&got, receipt) {
		t.Fatalf("receipt=%+v want=%+v err=%v", got, receipt, err)
	}
	if string(outcome["cached_receipt"]) != map[bool]string{true: "true", false: "false"}[cached] {
		t.Fatalf("cache metadata=%s", outcome["cached_receipt"])
	}
}

func assertNativeMCPOutcome(t *testing.T, result *mcp.CallToolResult, receipt *domain.Receipt, cached bool) {
	t.Helper()
	assertNativeOutcome(t, result.StructuredContent, receipt, cached)
	if len(result.Content) != 1 {
		t.Fatalf("content=%+v", result.Content)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content=%T", result.Content[0])
	}
	var content any
	if err := json.Unmarshal([]byte(text.Text), &content); err != nil {
		t.Fatal(err)
	}
	assertNativeOutcome(t, content, receipt, cached)
}

func TestDesktopActNativeOutcomeWire(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(map[bool]string{false: "uncached", true: "cached"}[cached], func(t *testing.T) {
			receipt := desktopOutcomeReceipt(false)
			var requests []app.ConsoleInputRequest
			a := desktopMCPServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/console/input" {
					t.Errorf("unexpected route=%s", r.URL.Path)
				}
				var req app.ConsoleInputRequest
				decodeDesktopMCPRequest(t, r, &req)
				requests = append(requests, req)
				if cached {
					w.Header().Set("X-AMC-Cached-Receipt", "true")
				}
				if err := json.NewEncoder(w).Encode(receipt); err != nil {
					t.Error(err)
				}
			})
			in := DesktopActInput{Target: desktopVMA, Input: &domain.ConsoleInput{Kind: "key", Key: "enter"}, LabGrantID: "explicit-grant", Reason: "synthetic input", IdempotencyKey: receipt.IdempotencyKey, Deadline: "2026-10-03T12:01:00Z"}
			for range 2 {
				result := callDesktopMCPTool(t, a, "desktop_act", in)
				assertNativeMCPOutcome(t, result, receipt, cached)
			}
			want := app.ConsoleInputRequest{Target: in.Target, Input: *in.Input, LabGrantID: in.LabGrantID, Reason: in.Reason, IdempotencyKey: in.IdempotencyKey, Deadline: in.Deadline}
			if !reflect.DeepEqual(requests, []app.ConsoleInputRequest{want, want}) {
				t.Fatalf("retry payloads=%+v", requests)
			}
		})
	}
}

func TestDesktopActNativeFailureOutcome(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(map[bool]string{false: "uncached", true: "cached"}[cached], func(t *testing.T) {
			receipt := desktopOutcomeReceipt(true)
			calls := 0
			a := desktopMCPServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/v1/console/input" {
					t.Errorf("failure triggered observation or redispatch: %s", r.URL.Path)
				}
				w.WriteHeader(http.StatusBadRequest)
				env := daemon.DesktopErrorEnvelope{ErrorEnvelope: daemon.ErrorEnvelope{SchemaVersion: "1", Error: daemon.ErrorField{Category: "console_failed", Message: "synthetic private error"}}, Receipt: receipt, CachedReceipt: cached}
				if err := json.NewEncoder(w).Encode(env); err != nil {
					t.Error(err)
				}
			})
			in := DesktopActInput{Target: desktopVMA, Input: &domain.ConsoleInput{Kind: "key", Key: "enter"}, LabGrantID: "explicit-grant", IdempotencyKey: receipt.IdempotencyKey, ObserveAfter: true}
			result, out, err := a.DesktopAct(t.Context(), nil, in)
			if err != nil || result == nil || !result.IsError || calls != 1 || out.Observation != nil {
				t.Fatalf("result=%+v out=%+v calls=%d err=%v", result, out, calls, err)
			}
			assertNativeOutcome(t, out, receipt, cached)
			data, err := json.Marshal(result)
			if err != nil || !strings.Contains(string(data), string(receipt.ReceiptID)) || strings.Contains(string(data), "synthetic private error") {
				t.Fatalf("failure receipt/redaction=%s err=%v", data, err)
			}
		})
	}
}

func TestDesktopActSemanticOutcomeWire(t *testing.T) {
	response := domain.DesktopResponse{RequestID: "synthetic-request", Success: true, SessionID: 7, Elevated: true, Text: "synthetic text"}
	receipt := desktopOutcomeReceipt(false)
	receipt.OperationKind = "desktop.action"
	a := desktopMCPServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/desktop/action" {
			t.Errorf("unexpected route=%s", r.URL.Path)
		}
		if err := json.NewEncoder(w).Encode(app.DesktopActionResult{Response: response, Receipt: receipt, CachedReceipt: true}); err != nil {
			t.Error(err)
		}
	})
	in := DesktopActInput{Target: desktopVMA, Action: &domain.DesktopRequest{Action: "clipboard.set"}, LabGrantID: "explicit-grant"}
	result := callDesktopMCPTool(t, a, "desktop_act", in)
	outcome := desktopOutcomeJSON(t, result.StructuredContent)
	var got app.DesktopActionResult
	data, err := json.Marshal(outcome)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &got); err != nil || !reflect.DeepEqual(got, app.DesktopActionResult{Response: response, Receipt: receipt, CachedReceipt: true}) {
		t.Fatalf("semantic outcome=%s err=%v", data, err)
	}
}

func TestDesktopActOutcomeSchema(t *testing.T) {
	for _, tool := range getExposedTools(t.Context(), t).Tools {
		if tool.Name != "desktop_act" {
			continue
		}
		data, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Properties map[string]struct {
				Required   []string                   `json:"required"`
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(data, &schema); err != nil {
			t.Fatal(err)
		}
		outcome := schema.Properties["result"]
		if !reflect.DeepEqual(outcome.Required, []string{"cached_receipt"}) || outcome.Properties["receipt"] == nil || outcome.Properties["response"] == nil {
			t.Fatalf("outcome schema=%s", data)
		}
		var response struct {
			Required []string `json:"required"`
		}
		if err := json.Unmarshal(outcome.Properties["response"], &response); err != nil || !reflect.DeepEqual(response.Required, []string{"request_id", "success", "session_id", "elevated"}) {
			t.Fatalf("guest response schema=%s err=%v", outcome.Properties["response"], err)
		}
		return
	}
	t.Fatal("desktop_act missing from advertised tools")
}
