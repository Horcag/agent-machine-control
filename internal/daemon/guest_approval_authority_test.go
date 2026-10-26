package daemon

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/guest/stream"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/sessions"
)

// Exercise the production identity provider: an echoing config fixture conceals
// mismatches between a requested beneficiary and its current authenticated actor.
func TestGuestApprovalUsesCurrentBeneficiaryAuthority(t *testing.T) {
	provider, _, _, _ := newGuestConfigurationFixture(t)
	sd := provider.server.stateDir
	fixture := &guestHTTPServiceFixture{
		root:       stream.RootIdentity{Path: `C:\AMC\synthetic`, Volume: 7, FileID: strings.Repeat("a", 32), CreationTime: 100, Owner: "S-1-5-21-synthetic", SDDLHash: strings.Repeat("d", 64)},
		worker:     stream.WorkerIdentity{PID: 123, BirthTicks: 638000000000000001, Image: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, SID: "S-1-5-21-synthetic"},
		privileged: true,
	}
	ref := "local:11111111-1111-4111-8111-111111111111"
	record, err := provider.workspaceRecord(t.Context(), domain.MachineRef(ref))
	if err != nil {
		t.Fatal(err)
	}
	record.Workspace = fixture.root
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(ref))
	name := hex.EncodeToString(digest[:])
	if err := writeGuestPrivateFixture(t, filepath.Join(sd.Root(), "guest-workspaces", name+".json"), data); err != nil {
		t.Fatal(err)
	}
	coordinator := app.NewSessionService(nil, fixture, lease.NewManager(sd.LeasesDir()), audit.NewStore(sd.AuditDir()), receipt.NewStore(sd.ReceiptsDir()), approval.NewStore(sd.ApprovalsDir()), app.WithSessionMutationJournal(sessions.NewMutationJournal(sd.SessionsDir())))
	server := provider.server
	server.semaphore = make(chan struct{}, 8)
	server.guestService = app.NewGuestService(coordinator, fixture, &provider, fixture, app.NewGuestStore(filepath.Join(sd.Root(), "guest")))
	endpoint := httptest.NewServer(server.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/approval" {
			server.handleGuestApproval(w, r)
			return
		}
		server.handleGuestOperation(w, r)
	})))
	t.Cleanup(endpoint.Close)
	operator, err := auth.ReadTokenFile(sd.AuthDir(), auth.TokenTypeOperator)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := auth.ReadTokenFile(sd.AuthDir(), auth.TokenTypeAgentMCP)
	if err != nil {
		t.Fatal(err)
	}
	p := app.GuestOperationParams{Target: ref, Operation: "prepare", Reason: "approve exact guest preparation", IdempotencyKey: "real-beneficiary-prepare"}
	request := GuestApprovalRequest{Params: p, Beneficiary: string(auth.AgentMCPPrincipal), ValidForMillis: 60000}
	first := issueGuestApprovalTwice(t, endpoint.URL+"/approval", operator, request)
	p.ApprovalID, p.Deadline = first.Grant.ApprovalID, first.Grant.Deadline
	final, _ := invokeGuestHTTPFixture(t, endpoint.URL+"/operation", agent, p, nil)
	if final.Error != "" || final.Result.Receipt == nil || final.Result.Receipt.Outcome.Status != domain.OutcomeSuccess || strings.Join(fixture.operations, ",") != "observe" {
		t.Fatalf("issued beneficiary could not execute: %+v operations=%v", final, fixture.operations)
	}
	final, _ = invokeGuestHTTPFixture(t, endpoint.URL+"/operation", agent, p, nil)
	if final.Error != "" || final.Result.Receipt == nil || strings.Join(fixture.operations, ",") != "observe" {
		t.Fatalf("exact execution retry dispatched again: %+v operations=%v", final, fixture.operations)
	}
	assertGuestApprovedPayload(t, endpoint, operator, agent, ref, fixture, request)
	request.Params.IdempotencyKey = "agent-cannot-issue"
	response := requestGuestApproval(t, endpoint.URL+"/approval", agent, request)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("agent issued operator authority: status=%d", response.StatusCode)
	}
}

func requestGuestApproval(t *testing.T, endpoint, token string, value GuestApprovalRequest) *http.Response {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestGuestApprovalReportsAuthorityFailuresBeforeIssuance(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		category string
	}{
		{"revoked authority", app.ErrGuestAuthority, "guest_authority"},
		{"missing workspace", app.ErrGuestWorkspaceUnprovisioned, "guest_workspace_unprovisioned"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoint, token, fixture := newGuestHTTPServiceFixture(t)
			fixture.configErr = tc.err
			request := GuestApprovalRequest{Params: app.GuestOperationParams{Operation: "prepare", Reason: "synthetic approval failure", IdempotencyKey: "failed-approval"}, Beneficiary: "self", ValidForMillis: time.Minute.Milliseconds()}
			response := requestGuestApproval(t, endpoint.URL+"/approval", token, request)
			var body struct {
				Error struct {
					Category string `json:"category"`
				} `json:"error"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&body)
			_ = response.Body.Close()
			if response.StatusCode != http.StatusForbidden || decodeErr != nil || body.Error.Category != tc.category || len(fixture.operations) != 0 {
				t.Fatalf("known authority failure: status=%d body=%+v operations=%v err=%v", response.StatusCode, body, fixture.operations, decodeErr)
			}
		})
	}
}

func issueGuestApprovalTwice(t *testing.T, endpoint, operator string, request GuestApprovalRequest) GuestApprovalResponse {
	t.Helper()
	var first GuestApprovalResponse
	for attempt := range 2 {
		response := requestGuestApproval(t, endpoint, operator, request)
		var issued GuestApprovalResponse
		decodeErr := json.NewDecoder(response.Body).Decode(&issued)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK || decodeErr != nil || issued.Grant == nil || issued.Receipt == nil {
			t.Fatalf("issuance attempt %d: status=%d response=%+v err=%v", attempt, response.StatusCode, issued, decodeErr)
		}
		if attempt == 0 {
			first = issued
		} else if first.Grant.ApprovalID != issued.Grant.ApprovalID || first.Receipt.ReceiptID != issued.Receipt.ReceiptID {
			t.Fatal("exact issuance retry changed authority")
		}
	}
	return first
}

func assertGuestApprovedPayload(t *testing.T, endpoint *httptest.Server, operator, agent, ref string, fixture *guestHTTPServiceFixture, request GuestApprovalRequest) {
	t.Helper()
	command, payload := guestHTTPCase("exec", "")
	command.Target = ref
	request.Params = command
	response := requestGuestApproval(t, endpoint.URL+"/approval", operator, request)
	var execution GuestApprovalResponse
	decodeErr := json.NewDecoder(response.Body).Decode(&execution)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || decodeErr != nil || execution.Grant == nil {
		t.Fatalf("payload approval: status=%d err=%v", response.StatusCode, decodeErr)
	}
	command.ApprovalID, command.Deadline = execution.Grant.ApprovalID, execution.Grant.Deadline
	final, output := invokeGuestHTTPFixture(t, endpoint.URL+"/operation", agent, command, payload)
	if final.Error != "" || final.Result.Receipt == nil || final.Result.Receipt.Outcome.Status != domain.OutcomeSuccess || string(output) != "verified command output" || strings.Join(fixture.operations, ",") != "observe,exec" {
		t.Fatalf("approved payload failed: %+v operations=%v", final, fixture.operations)
	}
}
