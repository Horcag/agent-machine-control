package hyperv_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/backends/hyperv"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestRefreshTrustedHostsDelegatesAppCoordinatorAndInjectsLocalRoute(t *testing.T) {
	inv, err := app.NewTrustedInventory([]app.HostEntry{
		{ID: "host-a", Address: "alpha.example", Enabled: true, QueryTimeout: time.Second},
		{ID: "host-b", Address: "bravo.example", Enabled: true, QueryTimeout: time.Second},
	})
	if err != nil {
		t.Fatalf("NewTrustedInventory failed: %v", err)
	}
	var active atomic.Int32
	var maxActive atomic.Int32
	exec := &mockExecutor{
		executeFn: func(_ context.Context, _ string, _ []string, env []string) ([]byte, []byte, error) {
			trackActive(&active, &maxActive)
			defer active.Add(-1)
			return thinFactoryPayload(hostAddress(env))
		},
	}
	snapshots, err := hyperv.RefreshTrustedHosts(context.Background(), inv, 1, hyperv.WithHostObserverExecutor(exec))
	if err != nil {
		t.Fatalf("RefreshTrustedHosts failed: %v", err)
	}
	gotIDs := []domain.HostID{snapshots[0].HostID, snapshots[1].HostID, snapshots[2].HostID}
	wantIDs := []domain.HostID{domain.LocalHostID, "host-a", "host-b"}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("snapshot order = %v, want %v", gotIDs, wantIDs)
	}
	if maxActive.Load() != 1 {
		t.Fatalf("expected app concurrency cap to be enforced, got %d", maxActive.Load())
	}
	if snapshots[0].Machines[0].HostID != domain.LocalHostID || snapshots[0].Machines[0].Locator.HostID != domain.LocalHostID {
		t.Fatalf("local route was not stamped: %+v", snapshots[0].Machines[0])
	}
	if snapshots[1].Machines[0].Locator.String() != "host-a:aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa" {
		t.Fatalf("remote host-a locator not stamped: %+v", snapshots[1].Machines[0])
	}
	if snapshots[2].Health != app.HostHealthAccessDenied {
		t.Fatalf("expected host-b access denied, got %+v", snapshots[2])
	}
}

func TestRefreshTrustedHostsPreservesExplicitLocalDisable(t *testing.T) {
	inv, err := app.NewTrustedInventory([]app.HostEntry{
		{ID: domain.LocalHostID, Address: "local", Enabled: false},
		{ID: "host-a", Address: "alpha.example", Enabled: true, QueryTimeout: time.Second},
	})
	if err != nil {
		t.Fatalf("NewTrustedInventory failed: %v", err)
	}
	var calls atomic.Int32
	exec := &mockExecutor{
		executeFn: func(_ context.Context, _ string, _ []string, env []string) ([]byte, []byte, error) {
			calls.Add(1)
			if hostAddress(env) == "" {
				t.Fatal("explicitly disabled local host should not be probed")
			}
			return thinFactoryPayload(hostAddress(env))
		},
	}
	snapshots, err := hyperv.RefreshTrustedHosts(context.Background(), inv, 2, hyperv.WithHostObserverExecutor(exec))
	if err != nil {
		t.Fatalf("RefreshTrustedHosts failed: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected only remote host probe, got %d calls", calls.Load())
	}
	if snapshots[0].HostID != domain.LocalHostID || snapshots[0].Health != app.HostHealthStale {
		t.Fatalf("disabled local host should be stale snapshot, got %+v", snapshots[0])
	}
}

func trackActive(active, maxActive *atomic.Int32) {
	current := active.Add(1)
	for {
		seen := maxActive.Load()
		if current <= seen || maxActive.CompareAndSwap(seen, current) {
			return
		}
	}
}

func thinFactoryPayload(address string) ([]byte, []byte, error) {
	switch address {
	case "":
		return []byte(`{"schema_version":"1","machines":[{"id":"11111111-1111-4111-8111-111111111111","name":"local-vm","state":"Off","generation":2,"version":"10.0"}]}`), nil, nil
	case "alpha.example":
		return []byte(`{"schema_version":"1","machines":[{"id":"aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa","name":"vm-a","state":"Off","generation":2,"version":"10.0"}]}`), nil, nil
	case "bravo.example":
		return []byte(`{"schema_version":"1","error_category":"access_denied"}`), nil, nil
	default:
		return nil, nil, errors.New("unexpected host")
	}
}

func hostAddress(env []string) string {
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, hyperv.HostAddressEnvVar+"="); ok {
			return value
		}
	}
	return ""
}
