package daemon_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

type approvalEffectsBackend struct {
	mockDaemonBackend
	mu       sync.Mutex
	lists    int
	starts   []string
	stops    []string
	creates  []string
	restores []string
}

func (b *approvalEffectsBackend) ListMachines(ctx context.Context) ([]domain.MachineObservation, error) {
	b.mu.Lock()
	b.lists++
	b.mu.Unlock()
	return b.mockDaemonBackend.ListMachines(ctx)
}

func (b *approvalEffectsBackend) Capabilities(context.Context, string) (domain.CapabilitySet, error) {
	return domain.DirectMachineCapabilities(), nil
}

func (b *approvalEffectsBackend) ListCheckpoints(context.Context, string) ([]domain.CheckpointObservation, error) {
	return nil, nil
}

func (b *approvalEffectsBackend) StartMachine(_ context.Context, id string) (domain.MachineObservation, error) {
	b.mu.Lock()
	b.starts = append(b.starts, id)
	b.mu.Unlock()
	return domain.MachineObservation{ID: id, State: domain.MachineStateRunning}, nil
}

func (b *approvalEffectsBackend) StopMachine(_ context.Context, id, mode string) (domain.MachineObservation, error) {
	b.mu.Lock()
	b.stops = append(b.stops, id+":"+mode)
	b.mu.Unlock()
	return domain.MachineObservation{ID: id, State: domain.MachineStateOff}, nil
}

func (b *approvalEffectsBackend) CreateCheckpoint(_ context.Context, id, name string) (domain.CheckpointObservation, error) {
	b.mu.Lock()
	b.creates = append(b.creates, id+":"+name)
	b.mu.Unlock()
	return domain.CheckpointObservation{
		ID: "e4a523d4-6b99-4d62-a5e2-4752c0f20001", Name: name, VMID: id,
		CreatedAt: time.Now().UTC(), ObservedAt: time.Now().UTC(), ObservationType: domain.ObservationObserved,
	}, nil
}

func (b *approvalEffectsBackend) RestoreCheckpoint(_ context.Context, id, checkpointID string) (domain.MachineObservation, error) {
	b.mu.Lock()
	b.restores = append(b.restores, id+":"+checkpointID)
	b.mu.Unlock()
	return domain.MachineObservation{ID: id, State: domain.MachineStateOff}, nil
}

func setupOperationApprovalServer(t *testing.T, backend app.Backend) (*daemon.Server, *client.Client, string) {
	return setupOperationApprovalServerWithClock(t, backend, nil)
}

func setupOperationApprovalServerWithClock(t *testing.T, backend app.Backend, clock func() time.Time) (*daemon.Server, *client.Client, string) {
	t.Helper()
	stateDir := missingDaemonStateRoot(t)
	seedDaemonTestTarget(t, stateDir)
	server, err := daemon.NewServer(daemon.Config{
		StateDir: stateDir, ListenAddr: "127.0.0.1:0", Backend: backend, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	token, err := auth.ReadTokenFile(stateDir+"/auth", auth.TokenTypeOperator)
	if err != nil {
		t.Fatal(err)
	}
	return server, client.New(server.Endpoint(), token), stateDir
}

//nolint:cyclop // One transport scenario proves both allowed beneficiaries and operator-only issuance.
func TestDaemonOperationApprovalSelfAndMCPExecution(t *testing.T) {
	srv, endpoint, operatorToken, agentToken := setupTestServer(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()
	operator := client.New(endpoint, operatorToken)
	agent := client.New(endpoint, agentToken)

	selfGrant, err := operator.IssueOperationApproval(context.Background(), daemon.OperationApprovalIssueRequest{
		Kind: "machine.stop", Target: "default", Reason: "authorize exact operator turn off",
		IdempotencyKey: "daemon-operation-approval-self", ValidForMillis: 60_000,
		Beneficiary: "self", Parameters: map[string]any{"mode": "turn-off"},
	})
	if err != nil {
		t.Fatalf("IssueOperationApproval(self): %v", err)
	}
	for _, reference := range []string{"primary", daemonTestVMID, "local:" + daemonTestVMID} {
		retryRequest := daemon.OperationApprovalIssueRequest{
			Kind: "machine.stop", Target: reference, Reason: "authorize exact operator turn off",
			IdempotencyKey: "daemon-operation-approval-self", ValidForMillis: 60_000,
			Beneficiary: "self", Parameters: map[string]any{"mode": "turn-off"},
		}
		retry, retryErr := operator.IssueOperationApproval(context.Background(), retryRequest)
		if retryErr != nil || retry.ApprovalID != selfGrant.ApprovalID || retry.Deadline != selfGrant.Deadline {
			t.Fatalf("canonical issuance retry %q = %+v err=%v", reference, retry, retryErr)
		}
	}
	selfDeadline, err := time.Parse(time.RFC3339Nano, selfGrant.Deadline)
	if err != nil {
		t.Fatal(err)
	}
	selfOperation, err := operator.CreateOperation(context.Background(), daemon.CreateOperationRequest{
		Kind: "machine.stop", Target: "primary", Reason: "authorize exact operator turn off",
		IdempotencyKey: "daemon-operation-approval-self", Deadline: &selfDeadline,
		ApprovalID: selfGrant.ApprovalID, Parameters: map[string]any{"mode": "turn-off"},
	})
	if err != nil {
		t.Fatalf("CreateOperation(self): %v", err)
	}
	selfFinal, err := operator.WaitOperation(context.Background(), selfOperation.OperationID, 10*time.Second, 0)
	if err != nil || selfFinal.State != "completed" {
		t.Fatalf("self final = %+v err=%v", selfFinal, err)
	}
	if selfFinal.ApprovalID != selfGrant.ApprovalID || selfFinal.Target == daemonTestVMID {
		t.Fatalf("self durable identity = %+v", selfFinal)
	}

	mcpGrant, err := operator.IssueOperationApproval(context.Background(), daemon.OperationApprovalIssueRequest{
		Kind: "machine.stop", Target: daemonTestVMID, Reason: "authorize exact MCP turn off",
		IdempotencyKey: "daemon-operation-approval-mcp", ValidForMillis: 60_000,
		Beneficiary: "agent:mcp-local", Parameters: map[string]any{"mode": "turn-off"},
	})
	if err != nil {
		t.Fatalf("IssueOperationApproval(mcp): %v", err)
	}
	mcpDeadline, _ := time.Parse(time.RFC3339Nano, mcpGrant.Deadline)
	mcpOperation, err := agent.CreateOperation(context.Background(), daemon.CreateOperationRequest{
		Kind: "machine.stop", Target: daemonTestVMID, Reason: "authorize exact MCP turn off",
		IdempotencyKey: "daemon-operation-approval-mcp", Deadline: &mcpDeadline,
		ApprovalID: mcpGrant.ApprovalID, Parameters: map[string]any{"mode": "turn-off"},
	})
	if err != nil {
		t.Fatalf("CreateOperation(mcp): %v", err)
	}
	mcpFinal, err := agent.WaitOperation(context.Background(), mcpOperation.OperationID, 10*time.Second, 0)
	if err != nil || mcpFinal.State != "completed" {
		t.Fatalf("MCP final = %+v err=%v", mcpFinal, err)
	}

	if _, err := agent.IssueOperationApproval(context.Background(), daemon.OperationApprovalIssueRequest{
		Kind: "machine.stop", Target: daemonTestVMID, Reason: "agent must not issue",
		IdempotencyKey: "daemon-operation-approval-agent-denied", ValidForMillis: 60_000,
		Parameters: map[string]any{"mode": "turn-off"},
	}); !errors.Is(err, client.ErrDenied) {
		t.Fatalf("agent issuance error = %v", err)
	}
	if _, err := operator.IssueOperationApproval(context.Background(), daemon.OperationApprovalIssueRequest{
		Kind: "machine.start", Target: daemonTestVMID, Reason: "rollback makes approval unnecessary",
		IdempotencyKey: "daemon-operation-approval-not-required", ValidForMillis: 60_000,
	}); !errors.Is(err, client.ErrConflict) {
		t.Fatalf("not-required issuance error = %v", err)
	}
}

func TestDaemonOperationApprovalStrictJSONAndReferencePairs(t *testing.T) {
	srv, endpoint, operatorToken, _ := setupTestServer(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	base := `{"kind":"machine.stop","target":"` + daemonTestVMID + `","reason":"strict approval request","idempotency_key":"strict-operation-approval","valid_for_ms":60000,"parameters":{"mode":"turn-off"}`
	cases := []string{
		base + `,"kind":"machine.start"}`,
		base + `,"unknown":true}`,
		base + `} trailing`,
	}
	for _, body := range cases {
		request, _ := http.NewRequest(http.MethodPost, endpoint+"/v1/operation-approvals", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+operatorToken)
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("strict issuance body status = %d for %q", response.StatusCode, body)
		}
	}

	oversized := base + `,"padding":"` + strings.Repeat("x", 70*1024) + `"}`
	request, _ := http.NewRequest(http.MethodPost, endpoint+"/v1/operation-approvals", strings.NewReader(oversized))
	request.Header.Set("Authorization", "Bearer "+operatorToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized issuance status = %d", response.StatusCode)
	}

	duplicateCreate := `{"kind":"machine.stop","kind":"machine.start","target":"` + daemonTestVMID + `","reason":"duplicate create","idempotency_key":"duplicate-create"}`
	request, _ = http.NewRequest(http.MethodPost, endpoint+"/v1/operations", bytes.NewBufferString(duplicateCreate))
	request.Header.Set("Authorization", "Bearer "+operatorToken)
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("duplicate create status = %d", response.StatusCode)
	}
	for name, rawBody := range map[string]string{
		"raw-approval":  `{"kind":"machine.stop","target":"` + daemonTestVMID + `","reason":"raw approval","idempotency_key":"raw-approval","approval":{"id":"forged"},"parameters":{"mode":"turn-off"}}`,
		"null-required": `{"kind":null,"target":"` + daemonTestVMID + `","reason":"null kind","idempotency_key":"null-kind"}`,
	} {
		request, _ = http.NewRequest(http.MethodPost, endpoint+"/v1/operations", strings.NewReader(rawBody))
		request.Header.Set("Authorization", "Bearer "+operatorToken)
		request.Header.Set("Content-Type", "application/json")
		response, err = http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s create status = %d", name, response.StatusCode)
		}
	}

	deadline := time.Now().UTC().Add(time.Minute)
	for name, payload := range map[string]any{
		"id-only": daemon.CreateOperationRequest{
			Kind: "machine.stop", Target: daemonTestVMID, Reason: "id only", IdempotencyKey: "id-only-reference",
			ApprovalID: "app-operation-0123456789abcdef0123456789abcdef", Parameters: map[string]any{"mode": "turn-off"},
		},
		"timeout-and-reference": daemon.CreateOperationRequest{
			Kind: "machine.stop", Target: daemonTestVMID, Reason: "mixed timeout", IdempotencyKey: "mixed-timeout-reference",
			ApprovalID: "app-operation-0123456789abcdef0123456789abcdef", Deadline: &deadline, TimeoutSeconds: 30,
			Parameters: map[string]any{"mode": "turn-off"},
		},
	} {
		data, _ := json.Marshal(payload)
		request, _ = http.NewRequest(http.MethodPost, endpoint+"/v1/operations", bytes.NewReader(data))
		request.Header.Set("Authorization", "Bearer "+operatorToken)
		request.Header.Set("Content-Type", "application/json")
		response, err = http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s status = %d", name, response.StatusCode)
		}
	}
}

//nolint:cyclop // The table intentionally verifies every supported privileged kind and shared identity invariant.
func TestDaemonOperationApprovalExecutesAllSupportedKindsWithCanonicalIdentity(t *testing.T) {
	backend := &approvalEffectsBackend{}
	server, operator, _ := setupOperationApprovalServer(t, backend)
	defer func() { _ = server.Shutdown(context.Background()) }()
	checkpointID := "e4a523d4-6b99-4d62-a5e2-4752c0f20001"
	tests := []struct {
		kind       string
		key        string
		parameters map[string]any
	}{
		{kind: "machine.start", key: "approved-dynamic-start"},
		{kind: "machine.stop", key: "approved-turn-off", parameters: map[string]any{"mode": "turn-off"}},
		{kind: "checkpoint.create", key: "approved-checkpoint-create", parameters: map[string]any{"name": "safe synthetic checkpoint"}},
		{kind: "checkpoint.restore", key: "approved-checkpoint-restore", parameters: map[string]any{"checkpoint_id": checkpointID}},
	}
	for _, test := range tests {
		reason := "execute " + test.kind + " with server approval"
		grant, err := operator.IssueOperationApproval(context.Background(), daemon.OperationApprovalIssueRequest{
			Kind: test.kind, Target: "default", Reason: reason, IdempotencyKey: test.key,
			ValidForMillis: 60_000, Beneficiary: "self", Parameters: test.parameters,
		})
		if err != nil {
			t.Fatalf("issue %s: %v", test.kind, err)
		}
		deadline, _ := time.Parse(time.RFC3339Nano, grant.Deadline)
		operation, err := operator.CreateOperation(context.Background(), daemon.CreateOperationRequest{
			Kind: test.kind, Target: daemonTestVMID, Reason: reason, IdempotencyKey: test.key,
			Deadline: &deadline, ApprovalID: grant.ApprovalID, Parameters: test.parameters,
		})
		if err != nil {
			t.Fatalf("submit %s: %v", test.kind, err)
		}
		terminal, err := operator.WaitOperation(context.Background(), operation.OperationID, 10*time.Second, 0)
		if err != nil || terminal.State != "completed" {
			t.Fatalf("terminal %s = %+v err=%v", test.kind, terminal, err)
		}
		receiptDTO, err := operator.GetReceipt(context.Background(), terminal.ReceiptID)
		if err != nil {
			t.Fatalf("receipt %s: %v", test.kind, err)
		}
		if receiptDTO.Target != "local:"+daemonTestVMID || len(receiptDTO.EvidenceRefs) != 1 || receiptDTO.EvidenceRefs[0] != grant.ApprovalID {
			t.Fatalf("receipt %s identity = %+v", test.kind, receiptDTO)
		}
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.starts) != 1 || backend.starts[0] != daemonTestVMID ||
		len(backend.stops) != 1 || backend.stops[0] != daemonTestVMID+":turn-off" ||
		len(backend.creates) != 1 || !strings.HasPrefix(backend.creates[0], daemonTestVMID+":") ||
		len(backend.restores) != 1 || backend.restores[0] != daemonTestVMID+":"+checkpointID {
		t.Fatalf("provider calls use non-canonical backend identity: starts=%v stops=%v creates=%v restores=%v", backend.starts, backend.stops, backend.creates, backend.restores)
	}
}

//nolint:cyclop // One transport scenario keeps coupled denial, receipt, and zero-effect assertions together.
func TestDaemonOperationApprovalReferenceFailuresAreDurableAndEffectFree(t *testing.T) {
	backend := &approvalEffectsBackend{}
	server, operator, stateDir := setupOperationApprovalServer(t, backend)
	defer func() { _ = server.Shutdown(context.Background()) }()
	agentToken, err := auth.ReadTokenFile(stateDir+"/auth", auth.TokenTypeAgentMCP)
	if err != nil {
		t.Fatal(err)
	}
	agent := client.New(server.Endpoint(), agentToken)

	issue := daemon.OperationApprovalIssueRequest{
		Kind: "machine.stop", Target: daemonTestVMID, Reason: "exact failure-bound turn off",
		IdempotencyKey: "approval-reference-failure-base", ValidForMillis: 60_000,
		Beneficiary: "self", Parameters: map[string]any{"mode": "turn-off"},
	}
	tests := []struct {
		name   string
		client *client.Client
	}{
		{name: "forged", client: operator},
		{name: "lower-precision-deadline-mismatch", client: operator},
		{name: "cross-actor", client: agent},
	}
	for index, test := range tests {
		exactIssue := issue
		exactIssue.IdempotencyKey += "-" + test.name
		grant, err := operator.IssueOperationApproval(context.Background(), exactIssue)
		if err != nil {
			t.Fatalf("issue %s: %v", test.name, err)
		}
		approvalID := grant.ApprovalID
		deadline, _ := time.Parse(time.RFC3339Nano, grant.Deadline)
		switch test.name {
		case "forged":
			approvalID = "app-operation-ffffffffffffffffffffffffffffffff"
		case "lower-precision-deadline-mismatch":
			deadline = deadline.Add(time.Second).Truncate(time.Second)
		}
		operation, err := test.client.CreateOperation(context.Background(), daemon.CreateOperationRequest{
			Kind: exactIssue.Kind, Target: exactIssue.Target, Reason: exactIssue.Reason,
			IdempotencyKey: exactIssue.IdempotencyKey,
			Deadline:       &deadline, ApprovalID: approvalID, Parameters: exactIssue.Parameters,
		})
		if err != nil {
			t.Fatalf("submit %s: %v", test.name, err)
		}
		terminal, err := test.client.WaitOperation(context.Background(), operation.OperationID, 10*time.Second, 0)
		if err != nil {
			t.Fatalf("wait %s: %v", test.name, err)
		}
		if terminal.State != "failed" || terminal.ErrorCategory != "approval_record_mismatch" || terminal.ReceiptID == "" || terminal.ApprovalID != approvalID {
			t.Fatalf("terminal %s = %+v", test.name, terminal)
		}
		receiptDTO, err := test.client.GetReceipt(context.Background(), terminal.ReceiptID)
		consumed, consumeErr := approval.NewStore(stateDir + "/approvals").IsConsumed(grant.ApprovalID)
		if err != nil || receiptDTO.Outcome.Status != domain.OutcomeDenied || receiptDTO.EvidenceRefs[0] != approvalID || consumeErr != nil || consumed {
			t.Fatalf("denial receipt %d = %+v err=%v; approval consumed=%v err=%v", index, receiptDTO, err, consumed, consumeErr)
		}
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.stops) != 0 {
		t.Fatalf("approval reference failures reached backend: %v", backend.stops)
	}
}
