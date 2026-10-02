package app_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/app"
	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/lease"
	"github.com/Horcag/agent-machine-control/internal/target"
)

func TestConsoleLabDispatchFencesClearAndIdenticalReEnrollment(t *testing.T) {
	f := newConsoleFixture(t)
	configureLab(f)
	store, prior := configureProtectedLabEnrollment(t, f)
	grant := issueLab(t, f)
	op, req, providerID := labOperation(t, grant)
	started, unblock, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, err := f.service.ExecuteLabMutation(context.Background(), req.Actor, grant.GrantID, op, req, providerID, func(context.Context) error { close(started); <-unblock; return nil })
		done <- err
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("dispatch failed before barrier: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("dispatch did not enter fence")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	publication, clearErr := store.Clear(ctx)
	cancel()
	close(unblock)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if publication.Committed || errors.Is(clearErr, target.ErrInsecureState) || (!errors.Is(clearErr, context.DeadlineExceeded) && !errors.Is(clearErr, lease.ErrLeaseConflict)) {
		t.Fatalf("clear crossed dispatch fence: %+v, %v", publication, clearErr)
	}
	if publication, err := store.Clear(context.Background()); err != nil || !publication.Durable {
		t.Fatalf("clear after dispatch: %+v, %v", publication, err)
	}
	if publication, err := store.Save(context.Background(), prior); err != nil || !publication.Durable {
		t.Fatalf("identical enrollment: %+v, %v", publication, err)
	}
	if _, err := f.service.Input(context.Background(), labActor(t), labRequest(grant)); err == nil || len(f.provider.inputs) != 0 {
		t.Fatalf("old grant dispatched after re-enrollment: %v", err)
	}
}

func configureProtectedLabEnrollment(t *testing.T, f consoleFixture) (*target.Store, target.Default) {
	t.Helper()
	store, err := target.NewStore(filepath.Join(f.root, "targets"))
	if err != nil {
		t.Fatal(err)
	}
	prior, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	app.WithConsoleLabEnrollmentIdentity(func(ctx context.Context, canonical domain.MachineRef) (string, error) {
		value, identity, err := store.EnrollmentIdentity(ctx)
		if err != nil || value.Locator.String() != string(canonical) {
			return "", app.ErrInvalidConsoleLabGrant
		}
		return identity, nil
	})(f.service)
	return store, prior
}
