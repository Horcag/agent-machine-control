package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/audit"
	"github.com/Horcag/agent-machine-control/internal/auth"
	"github.com/Horcag/agent-machine-control/internal/daemon"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/receipt"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/Horcag/agent-machine-control/internal/target"
)

const targetCommandVMID = "c4a523d4-6b99-4d62-a5e2-4752c0f20001"

//nolint:cyclop // The test verifies one end-to-end approval and enrollment flow.
func TestDirectTargetCommandsDiscoverApproveEnrollAndShow(t *testing.T) {
	service, coordinator, actor := targetCommandHarness(t)
	application := NewApp(nil, WithTargetService(service), WithTargetCoordinator(coordinator), WithActor(actor), WithPrompter(targetCommandPrompter{}), WithDirectMode(true))

	var stdout, stderr bytes.Buffer
	if code := application.Run([]string{"target", "candidates", "--json"}, &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("candidates code=%d stderr=%s", code, stderr.String())
	}
	var candidates targetCandidatesOutput
	if err := json.Unmarshal(stdout.Bytes(), &candidates); err != nil || len(candidates.Candidates) != 1 || candidates.Candidates[0].Locator != "local:"+targetCommandVMID {
		t.Fatalf("candidates=%+v err=%v", candidates, err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := application.Run([]string{"target", "approve", "enroll", targetCommandVMID, "--alias", "local", "--reason", "select test target", "--idempotency-key", "target-enroll", "--valid-for", "1m", "--json"}, &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("approve code=%d stderr=%s", code, stderr.String())
	}
	var grant targetApprovalOutput
	if err := json.Unmarshal(stdout.Bytes(), &grant); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := application.Run([]string{"target", "enroll", targetCommandVMID, "--alias", "local", "--reason", "select test target", "--idempotency-key", "target-enroll", "--approval-id", grant.ApprovalID, "--deadline", grant.Deadline, "--json"}, &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("enroll code=%d stderr=%s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := application.Run([]string{"target", "show", "--json"}, &stdout, &stderr); code != ExitSuccess || !bytes.Contains(stdout.Bytes(), []byte("local:"+targetCommandVMID)) {
		t.Fatalf("show code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if code := runTarget(context.Background(), nil, nil, domain.ActorContext{}, nil, true, "", []string{"show"}, &stdout, &stderr); code != ExitBackendUnavailable {
		t.Fatalf("nil service code=%d", code)
	}
	if code := runTarget(context.Background(), service, coordinator, actor, targetCommandPrompter{}, true, "", []string{"help"}, &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("help code=%d", code)
	}
	if code := runTargetCandidates(context.Background(), service, domain.ActorContext{}, nil, &stdout, &stderr); code != ExitDenied {
		t.Fatalf("candidate denial code=%d", code)
	}
	if code := runTargetCandidates(context.Background(), service, actor, []string{"unexpected"}, &stdout, &stderr); code != ExitUsage {
		t.Fatalf("candidate usage code=%d", code)
	}
	if code := runTargetShow(context.Background(), service, []string{"unexpected"}, &stdout, &stderr); code != ExitUsage {
		t.Fatalf("show usage code=%d", code)
	}
	if code := runTargetApprove(context.Background(), service, nil, actor, targetCommandPrompter{}, true, "", []string{"enroll", targetCommandVMID, "--reason", "select test target", "--idempotency-key", "target-enroll-2", "--valid-for", "1m"}, &stdout, &stderr); code != ExitBackendUnavailable {
		t.Fatalf("missing coordinator approval code=%d", code)
	}
	if code := runTargetMutation(context.Background(), service, nil, actor, true, "", "clear", []string{"--reason", "clear target", "--idempotency-key", "target-clear", "--approval-id", "app-target-0123456789abcdef0123456789abcdef", "--deadline", "2026-08-31T01:00:00Z"}, &stdout, &stderr); code != ExitBackendUnavailable {
		t.Fatalf("missing coordinator mutation code=%d", code)
	}
	if exactAlias("") != nil || writeTargetApprovalJSON(&stdout, nil) != ExitBackendUnavailable || mapTargetCommandError(target.ErrAccessDenied, &stderr, "target") != ExitDenied {
		t.Fatal("target helper failure behavior changed")
	}
}

func TestDaemonTargetCommandsUseOperatorClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/target-approvals":
			_, _ = w.Write([]byte(`{"schema_version":"1","approval_id":"app-target-0123456789abcdef0123456789abcdef","deadline":"2026-08-31T01:00:00Z","expires_at":"2026-08-31T01:00:00Z","operation":{"kind":"target.enroll","target":"local:c4a523d4-6b99-4d62-a5e2-4752c0f20001","reason":"select test target","idempotency_key":"target-enroll","parameters":{}},"receipt":{}}`))
		case "/v1/target":
			_, _ = w.Write([]byte(`{"schema_version":"1","target":{"locator":"local:c4a523d4-6b99-4d62-a5e2-4752c0f20001","provider_vm_id":"c4a523d4-6b99-4d62-a5e2-4752c0f20001"}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	state := targetDaemonState(t, server.URL)

	var stdout, stderr bytes.Buffer
	if code := issueDaemonTargetApproval(context.Background(), state, "target.enroll", targetCommandVMID, []string{"local"}, "select test target", "target-enroll", time.Minute, false, &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("daemon approval code=%d stderr=%s", code, stderr.String())
	}
	if code := executeDaemonTargetMutation(context.Background(), state, "enroll", targetCommandVMID, []string{"local"}, "select test target", "target-enroll", "app-target-0123456789abcdef0123456789abcdef", "2026-08-31T01:00:00Z", true, &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("daemon enroll code=%d stderr=%s", code, stderr.String())
	}
	if code := issueDaemonTargetApproval(context.Background(), state, "target.enroll", targetCommandVMID, []string{"local"}, "select test target", "target-enroll", time.Minute, true, &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("daemon approval JSON code=%d stderr=%s", code, stderr.String())
	}
	if code := executeDaemonTargetMutation(context.Background(), state, "clear", "", nil, "clear target", "target-clear", "app-target-0123456789abcdef0123456789abcdef", "2026-08-31T01:00:00Z", false, &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("daemon clear code=%d stderr=%s", code, stderr.String())
	}
	if code := issueDaemonTargetApproval(context.Background(), t.TempDir(), "target.enroll", targetCommandVMID, nil, "select test target", "target-error", time.Minute, false, &stdout, &stderr); code != ExitBackendUnavailable {
		t.Fatalf("daemon discovery error code=%d", code)
	}
}

type targetCommandPrompter struct{}

func (targetCommandPrompter) PromptConfirmation(string) bool { return true }

func targetCommandHarness(t *testing.T) (*app.TargetService, *app.TargetCoordinator, domain.ActorContext) {
	t.Helper()
	state, err := statedir.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := state.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	store, err := target.NewStore(state.TargetsDir())
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := app.NewTrustedInventory(nil)
	if err != nil {
		t.Fatal(err)
	}
	locator, err := domain.NewMachineLocator(domain.LocalHostID, targetCommandVMID)
	if err != nil {
		t.Fatal(err)
	}
	observation := domain.MachineObservation{HostID: domain.LocalHostID, Locator: locator, ID: targetCommandVMID, Name: "test-target", State: domain.MachineStateOff, RawState: "Off", Generation: 2, Version: "10.0", Capabilities: domain.ReadOnlyMachineCapabilities(), ObservedAt: time.Now().UTC(), ObservationType: domain.ObservationObserved}
	service, err := app.NewTargetService(inventory, store, app.WithTargetRefresh(func(context.Context) error {
		return inventory.ApplySnapshot(app.HostSnapshot{HostID: domain.LocalHostID, Health: app.HostHealthObserved, Machines: []domain.MachineObservation{observation}})
	}))
	if err != nil {
		t.Fatal(err)
	}
	journal, err := target.NewMutationJournal(state.TargetsDir())
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := app.NewTargetCoordinator(service, journal, audit.NewStore(state.AuditDir()), receipt.NewStore(state.ReceiptsDir()), approval.NewStore(state.ApprovalsDir()))
	if err != nil {
		t.Fatal(err)
	}
	scopes := domain.NewScopeSet(domain.ScopeTargetAdmin)
	actor, err := domain.NewActorContext("operator:test", "operator:test", scopes, scopes)
	if err != nil {
		t.Fatal(err)
	}
	return service, coordinator, actor
}

func targetDaemonState(t *testing.T, endpoint string) string {
	t.Helper()
	state, err := statedir.Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := state.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.LoadOrCreate(state.AuthDir()); err != nil {
		t.Fatal(err)
	}
	if err := daemon.WriteEndpointFile(state.DaemonDir(), daemon.EndpointRecord{SchemaVersion: daemon.SchemaVersion, PID: 1, RuntimeID: "target-test", StartedAt: time.Now().UTC(), Endpoint: endpoint}); err != nil {
		t.Fatal(err)
	}
	return state.Root()
}
