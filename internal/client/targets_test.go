package client_test

import (
	"context"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/client"
	"github.com/Horcag/agent-machine-control/internal/daemon"
)

func TestClientTargetApprovalMutationAndShow(t *testing.T) {
	srv, stateDir := setupDaemon(t)
	defer func() { _ = srv.Shutdown(context.Background()) }()
	cl, err := client.Discover(stateDir, client.TokenTypeOperator)
	if err != nil {
		t.Fatal(err)
	}
	shown, err := cl.GetTarget(context.Background())
	if err != nil || shown.Target.Locator != "local:"+clientTestVMID {
		t.Fatalf("GetTarget = %+v, %v", shown, err)
	}
	clearIssue := daemon.TargetApprovalIssueRequest{
		Kind: "clear", Reason: "clear client target authority", IdempotencyKey: "client-target-clear", ValidForMillis: 60_000,
	}
	clearGrant, err := cl.IssueTargetApproval(context.Background(), clearIssue)
	if err != nil {
		t.Fatal(err)
	}
	cleared, err := cl.ClearTarget(context.Background(), daemon.TargetMutationRequest{
		Reason: clearIssue.Reason, IdempotencyKey: clearIssue.IdempotencyKey,
		ApprovalID: clearGrant.ApprovalID, Deadline: clearGrant.Deadline,
	})
	if err != nil || cleared.Receipt == nil {
		t.Fatalf("ClearTarget = %+v, %v", cleared, err)
	}
	enrollIssue := daemon.TargetApprovalIssueRequest{
		Kind: "enroll", Aliases: []string{"client-primary"}, Reason: "enroll client target authority",
		IdempotencyKey: "client-target-enroll", ValidForMillis: 60_000,
	}
	enrollGrant, err := cl.IssueTargetApproval(context.Background(), enrollIssue)
	if err != nil {
		t.Fatal(err)
	}
	enrolled, err := cl.EnrollTarget(context.Background(), daemon.TargetMutationRequest{
		Aliases: enrollIssue.Aliases, Reason: enrollIssue.Reason, IdempotencyKey: enrollIssue.IdempotencyKey,
		ApprovalID: enrollGrant.ApprovalID, Deadline: enrollGrant.Deadline,
	})
	if err != nil || enrolled.Target.Locator != "local:"+clientTestVMID || enrolled.Receipt == nil {
		t.Fatalf("EnrollTarget = %+v, %v", enrolled, err)
	}
}
