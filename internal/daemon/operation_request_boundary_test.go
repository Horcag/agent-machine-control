package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/daemon"
)

func TestCreateOperationRequiredFieldsFailBeforeTargetResolution(t *testing.T) {
	backend := &approvalEffectsBackend{}
	server, _, stateDir := setupOperationApprovalServer(t, backend)
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	token, err := auth.ReadTokenFile(filepath.Join(stateDir, "auth"), auth.TokenTypeOperator)
	if err != nil {
		t.Fatal(err)
	}

	for _, field := range []string{"kind", "target", "reason", "idempotency_key"} {
		for _, form := range []string{"omitted", "null", "empty"} {
			t.Run(field+"-"+form, func(t *testing.T) {
				body := map[string]any{
					"kind": "machine.start", "target": daemonTestVMID,
					"reason": "strict required field", "idempotency_key": "strict-required-field",
				}
				switch form {
				case "omitted":
					delete(body, field)
				case "null":
					body[field] = nil
				case "empty":
					body[field] = ""
				}
				payload, marshalErr := json.Marshal(body)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				if status := sendRawCreateOperation(t, server.Endpoint(), token, payload); status != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400", status)
				}
			})
		}
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.lists != 0 || len(backend.starts) != 0 {
		t.Fatalf("rejected fields crossed target/backend boundary: lists=%d starts=%v", backend.lists, backend.starts)
	}
	assertNoOperationRecords(t, stateDir)
}

func TestCreateOperationExplicitDefaultResolvesTarget(t *testing.T) {
	backend := &approvalEffectsBackend{}
	server, _, stateDir := setupOperationApprovalServer(t, backend)
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	token, err := auth.ReadTokenFile(filepath.Join(stateDir, "auth"), auth.TokenTypeOperator)
	if err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	backend.lists = 0
	backend.mu.Unlock()

	payload, err := json.Marshal(map[string]any{
		"kind": "machine.start", "target": "default", "reason": "explicit default target",
		"idempotency_key": "explicit-default-target",
	})
	if err != nil {
		t.Fatal(err)
	}
	status := sendRawCreateOperation(t, server.Endpoint(), token, payload)
	if status != http.StatusAccepted && status != http.StatusOK {
		t.Fatalf("status = %d, want accepted operation", status)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.lists == 0 {
		t.Fatal("explicit default did not resolve the enrolled target")
	}
}

func TestApprovalBoundOperationDeadlineSpellingFailsBeforeAuthorityResolution(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 123_400_000, time.UTC)
	backend := &approvalEffectsBackend{}
	server, operator, stateDir := setupOperationApprovalServerWithClock(t, backend, func() time.Time { return now })
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	token, err := auth.ReadTokenFile(filepath.Join(stateDir, "auth"), auth.TokenTypeOperator)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := operator.IssueOperationApproval(context.Background(), daemon.OperationApprovalIssueRequest{
		Kind: "machine.stop", Target: daemonTestVMID, Reason: "reject alternate deadline spelling",
		IdempotencyKey: "alternate-deadline-spelling", ValidForMillis: 60_000,
		Parameters: map[string]any{"mode": "turn-off"},
	})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := time.Parse(time.RFC3339Nano, grant.Deadline)
	if err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	backend.lists = 0
	backend.mu.Unlock()

	variants := map[string]string{
		"alternate-offset-same-instant": issued.In(time.FixedZone("UTC+01", 3600)).Format(time.RFC3339Nano),
		"redundant-fractional-zero":     strings.TrimSuffix(grant.Deadline, "Z") + "0Z",
	}
	for name, deadline := range variants {
		t.Run(name, func(t *testing.T) {
			body := map[string]any{
				"kind": "machine.stop", "target": daemonTestVMID,
				"reason": "reject alternate deadline spelling", "idempotency_key": "alternate-deadline-spelling",
				"approval_id": grant.ApprovalID, "deadline": deadline,
				"parameters": map[string]any{"mode": "turn-off"},
			}
			payload, marshalErr := json.Marshal(body)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if status := sendRawCreateOperation(t, server.Endpoint(), token, payload); status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 for %q", status, deadline)
			}
		})
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.lists != 0 || len(backend.stops) != 0 {
		t.Fatalf("non-canonical deadline crossed target/backend boundary: lists=%d stops=%v", backend.lists, backend.stops)
	}
	assertNoOperationRecords(t, stateDir)
	if _, err := os.Stat(filepath.Join(stateDir, "approvals", grant.ApprovalID+".json")); !os.IsNotExist(err) {
		t.Fatalf("approval consumption record exists or cannot be checked: %v", err)
	}
}

func sendRawCreateOperation(t *testing.T, endpoint, token string, payload []byte) int {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, endpoint+"/v1/operations", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	return response.StatusCode
}

func assertNoOperationRecords(t *testing.T, stateDir string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(stateDir, "operations"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			t.Fatalf("operation manager persisted %q for boundary-rejected request", entry.Name())
		}
	}
}
