package cli_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/cli"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/statedir"
	"github.com/Horcag/agent-machine-control/internal/target"
)

func TestCLIEnrolledReadUsesSingleExactObservation(t *testing.T) {
	observed := cliTestObservation()
	queries := 0
	backend := &mockObserver{listErr: errors.New("unrelated VM prevents fleet discovery"), inspectFn: func(_ context.Context, id string) (domain.MachineObservation, error) {
		queries++
		if id != observed.ID {
			t.Fatalf("unexpected provider GUID %q", id)
		}
		return observed, nil
	}}
	state, err := statedir.Resolve(filepath.Join(t.TempDir(), "state"))
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
	enrolled, err := target.NewDefault(observed.Locator, []string{"primary"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(t.Context(), enrolled); err != nil {
		t.Fatal(err)
	}
	inventory, err := app.NewTrustedInventory(nil)
	if err != nil {
		t.Fatal(err)
	}
	targets, err := app.NewTargetService(inventory, store, app.WithTargetObserver(backend), app.WithTargetRefresh(func(ctx context.Context) error { return app.RefreshLocalTrustedInventory(ctx, inventory, backend) }))
	if err != nil {
		t.Fatal(err)
	}
	application := cli.NewApp(app.NewDiscoveryService(backend), cli.WithTargetService(targets))
	for _, command := range [][]string{{"machine", "list", "--json"}, {"machine", "inspect", "primary", "--json"}} {
		var stdout, stderr bytes.Buffer
		before := queries
		if code := application.Run(command, &stdout, &stderr); code != cli.ExitSuccess {
			t.Fatalf("%v exit=%d error=%s", command, code, stderr.String())
		}
		if queries != before+1 || !bytes.Contains(stdout.Bytes(), []byte(observed.ID)) {
			t.Fatalf("%v queries=%d output=%s", command, queries-before, stdout.String())
		}
	}
	var stdout, stderr bytes.Buffer
	before := queries
	if code := application.Run([]string{"machine", "inspect", observed.Name}, &stdout, &stderr); code != cli.ExitNotFound || queries != before {
		t.Fatalf("foreign reference exit=%d queries=%d", code, queries-before)
	}
}
