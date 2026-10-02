package app_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
)

func TestConsoleLabActiveDiscoverySelectsOwnNewestAuthority(t *testing.T) {
	f := newConsoleFixture(t)
	configureLab(f)
	agent := labActor(t)
	status, err := f.service.ActiveLabGrant(context.Background(), agent, "default")
	if err != nil || status.State != "disabled" || status.Grant.GrantID != "" {
		t.Fatalf("missing authority = %+v, %v", status, err)
	}
	first := issueLab(t, f)
	*f.now = f.now.Add(time.Second)
	second, _, err := f.service.IssueLabGrant(context.Background(), f.actor, app.ConsoleLabGrantIssueRequest{Target: "default", Reason: "new disposable lab authority", IdempotencyKey: "newer-grant", Beneficiary: "agent:mcp-local", ValidForMillis: 60000})
	if err != nil {
		t.Fatal(err)
	}
	status, err = f.service.ActiveLabGrant(context.Background(), agent, "default")
	if err != nil || status.State != "active" || status.Grant.GrantID != second.GrantID {
		t.Fatalf("newest authority = %+v, %v", status, err)
	}
	operatorStatus, err := f.service.ActiveLabGrant(context.Background(), f.actor, "default")
	if err != nil || operatorStatus.State != "disabled" || operatorStatus.Grant.GrantID != "" {
		t.Fatalf("operator saw foreign beneficiary: %+v, %v", operatorStatus, err)
	}
	_, err = f.service.RevokeLabGrant(context.Background(), f.actor, app.ConsoleLabGrantRevokeRequest{GrantID: second.GrantID, Reason: "revoke newest grant", IdempotencyKey: "revoke-newer", Deadline: f.now.Add(time.Minute).Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	status, err = f.service.ActiveLabGrant(context.Background(), agent, "default")
	if err != nil || status.Grant.GrantID != first.GrantID {
		t.Fatalf("older active authority = %+v, %v", status, err)
	}
}

func TestConsoleLabActiveDiscoveryRejectsStaleAndOversizedDirectory(t *testing.T) {
	f := newConsoleFixture(t)
	_, epoch := configureLab(f)
	issueLab(t, f)
	*epoch = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	status, err := f.service.ActiveLabGrant(context.Background(), labActor(t), "default")
	if err != nil || status.State != "disabled" || status.Grant.GrantID != "" {
		t.Fatalf("stale authority = %+v, %v", status, err)
	}
	root, err := os.OpenRoot(filepath.Join(f.root, "console-lab-grants"))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for i := range 129 {
		if err := root.WriteFile(fmt.Sprintf("bounded-fixture-%03d", i), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.service.ActiveLabGrant(context.Background(), labActor(t), "default"); err == nil {
		t.Fatal("unbounded directory discovery accepted")
	}
}
