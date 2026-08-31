package daemon_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
)

func TestDaemonSessions_AgentApprovalReferenceBoundary(t *testing.T) {
	srv, endpoint, operatorToken, agentToken, _, fakeSSH := setupTestDaemonWithSSHConfig(t, guestssh.SanitizerConfig{})
	defer func() {
		if err := srv.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown daemon: %v", err)
		}
	}()
	defer fakeSSH.Close()

	target := "c4a523d4-6b99-4d62-a5e2-4752c0f20001"
	base := map[string]any{
		"target": target, "reason": "exercise agent approval boundary",
		"idempotency_key": "agent-approval-boundary-malformed", "timeout_seconds": 30,
	}
	base["approval"] = map[string]any{"id": "app-agent-forged", "unrecognized_field": "forged"}
	status, _ := doJSONReq(t, http.MethodPost, endpoint+"/v1/sessions", agentToken, base)
	if status != http.StatusBadRequest {
		t.Fatalf("unrecognized raw approval schema status = %d, want 400", status)
	}

	status, _ = doJSONReq(t, http.MethodPost, endpoint+"/v1/sessions", agentToken, daemon.SessionOpenRequest{
		Target: target, Reason: "reject raw agent approval", IdempotencyKey: "agent-approval-boundary-raw",
		Approval: &domain.Approval{ID: "app-agent-self-approved"},
	})
	if status != http.StatusForbidden {
		t.Fatalf("raw agent approval status = %d, want 403", status)
	}

	status, _ = doJSONReq(t, http.MethodPost, endpoint+"/v1/sessions", agentToken, daemon.SessionOpenRequest{
		Target: target, Reason: "load missing approval reference", IdempotencyKey: "agent-approval-boundary-reference",
		ApprovalID: "app-agent-reference-missing", Deadline: time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano),
	})
	if status != http.StatusForbidden {
		t.Fatalf("missing approval reference status = %d, want 403", status)
	}

	status, _ = doJSONReq(t, http.MethodPost, endpoint+"/v1/sessions", operatorToken, daemon.SessionOpenRequest{
		Target: target, Reason: "reject mixed approval inputs", IdempotencyKey: "agent-approval-boundary-mixed",
		ApprovalID: "app-agent-reference-mixed", Deadline: time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano), Approval: &domain.Approval{ID: "app-agent-raw-mixed"},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("mixed approval inputs status = %d, want 400", status)
	}

	status, data := doJSONReq(t, http.MethodGet, endpoint+"/v1/sessions", agentToken, nil)
	if status != http.StatusOK {
		t.Fatalf("list sessions status = %d body=%s", status, data)
	}
	var listed daemon.SessionListResponse
	if err := json.Unmarshal(data, &listed); err != nil || len(listed.Sessions) != 0 {
		t.Fatalf("agent approval failures created sessions: %+v err=%v", listed.Sessions, err)
	}
}
