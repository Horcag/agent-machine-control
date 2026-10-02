package mcpadapter

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPConsoleButtonDefaultsBeforeDispatch(t *testing.T) {
	for _, tool := range []string{"console_input", "desktop_act"} {
		t.Run(tool, func(t *testing.T) {
			for _, tc := range []struct{ name, kind, button, want string }{
				{"omitted-click", "click", "", "left"}, {"omitted-drag", "drag", "", "left"},
				{"click-right", "click", "right", "right"}, {"click-middle", "click", "middle", "middle"},
				{"drag-right", "drag", "right", "right"}, {"drag-middle", "drag", "middle", "middle"}, {"move-empty", "move", "", ""},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var got app.ConsoleInputRequest
					calls := 0
					a := desktopMCPServer(t, func(w http.ResponseWriter, r *http.Request) {
						calls++
						if r.URL.Path != "/v1/console/input" {
							t.Errorf("unexpected route %s", r.URL.Path)
						}
						decodeDesktopMCPRequest(t, r, &got)
						_ = json.NewEncoder(w).Encode(domain.Receipt{ReceiptID: "synthetic-button-receipt"})
					})
					input := domain.ConsoleInput{Kind: tc.kind, FrameID: "synthetic-frame", Button: tc.button, X: 4, Y: 2}
					args := map[string]any{"target": "default", "input": input, "reason": "synthetic button default", "idempotency_key": "button-key", "deadline": "2026-10-02T20:00:00Z", "lab_grant_id": "own-grant"}
					callDesktopMCPTool(t, a, tool, args)
					want := input
					want.Button = tc.want
					if calls != 1 || got.Input != want || got.Reason != args["reason"] || got.IdempotencyKey != args["idempotency_key"] || got.Deadline != args["deadline"] || got.LabGrantID != "own-grant" {
						t.Fatalf("calls=%d request=%+v want=%+v", calls, got, want)
					}
				})
			}
		})
	}
}

func TestMCPConsoleButtonOmittedAndLeftHaveIdenticalRetryPayload(t *testing.T) {
	for _, tool := range []string{"console_input", "desktop_act"} {
		for _, kind := range []string{"click", "drag"} {
			t.Run(tool+"/"+kind, func(t *testing.T) {
				var requests []app.ConsoleInputRequest
				a := desktopMCPServer(t, func(w http.ResponseWriter, r *http.Request) {
					var req app.ConsoleInputRequest
					decodeDesktopMCPRequest(t, r, &req)
					requests = append(requests, req)
					_ = json.NewEncoder(w).Encode(domain.Receipt{ReceiptID: "synthetic-retry-receipt"})
				})
				for _, button := range []string{"", "left"} {
					callDesktopMCPTool(t, a, tool, map[string]any{"target": "default", "input": domain.ConsoleInput{Kind: kind, FrameID: "synthetic-frame", Button: button}, "reason": "synthetic retry", "idempotency_key": "same-button-key", "deadline": "2026-10-02T20:00:00Z", "lab_grant_id": "own-grant"})
				}
				if len(requests) != 2 || requests[0] != requests[1] || !reflect.DeepEqual(domain.ConsoleInputParameters(requests[0].Input), domain.ConsoleInputParameters(requests[1].Input)) {
					t.Fatalf("retry binding changed: %+v", requests)
				}
			})
		}
	}
}

func TestMCPConsoleButtonInvalidInputsMakeNoCalls(t *testing.T) {
	for _, tool := range []string{"console_input", "desktop_act"} {
		invoke := func(a *Adapter, input domain.ConsoleInput) (*mcp.CallToolResult, error) {
			result, _, err := a.ConsoleInput(t.Context(), nil, ConsoleInputInput{Input: input})
			return result, err
		}
		if tool == "desktop_act" {
			invoke = func(a *Adapter, input domain.ConsoleInput) (*mcp.CallToolResult, error) {
				result, _, err := a.DesktopAct(t.Context(), nil, DesktopActInput{Input: &input})
				return result, err
			}
		}

		for _, tc := range []struct {
			name  string
			input domain.ConsoleInput
		}{
			{"unsupported-kind", domain.ConsoleInput{Kind: "scroll", FrameID: "synthetic-frame"}},
			{"invalid-button", domain.ConsoleInput{Kind: "click", FrameID: "synthetic-frame", Button: "foreign"}},
			{"move-with-button", domain.ConsoleInput{Kind: "move", FrameID: "synthetic-frame", Button: "left"}},
			{"keyboard-with-button", domain.ConsoleInput{Kind: "key", Key: "enter", Button: "left"}},
			{"missing-frame", domain.ConsoleInput{Kind: "click"}},
		} {
			t.Run(tool+"/"+tc.name, func(t *testing.T) {
				calls := 0
				a := desktopMCPServer(t, func(http.ResponseWriter, *http.Request) { calls++ })
				result, err := invoke(a, tc.input)
				if err != nil || result == nil || !result.IsError {
					t.Fatalf("result=%+v err=%v", result, err)
				}

				if calls != 0 {
					t.Fatalf("invalid input made %d calls", calls)
				}
			})
		}
	}
}

func TestDesktopActButtonDefaultPreservesCallerInput(t *testing.T) {
	for _, kind := range []string{"click", "drag"} {
		t.Run(kind, func(t *testing.T) {
			d := &desktopActDaemon{}
			a := desktopMCPServer(t, d.handler(t))
			input := domain.ConsoleInput{Kind: kind, FrameID: "synthetic-frame"}
			original := input
			result, _, err := a.DesktopAct(t.Context(), nil, DesktopActInput{Input: &input, LabGrantID: "own-grant"})
			if err != nil || result != nil || input != original || len(d.inputs) != 1 || d.inputs[0].Input.Button != "left" {
				t.Fatalf("input=%+v original=%+v requests=%+v result=%+v err=%v", input, original, d.inputs, result, err)
			}
		})
	}
}
