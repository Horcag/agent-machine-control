package sessions_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/sessions"
)

func TestManagerLifecycleSurvivesBackwardClockStep(t *testing.T) {
	createdAt := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	var wall atomic.Int64
	wall.Store(createdAt.UnixNano())
	directory := testSessionsDir(t)
	transport := &deadlineGuardTransport{channel: newDeadlineGuardChannel()}
	manager := sessions.NewManager(directory, transport, func() time.Time { return time.Unix(0, wall.Load()) })
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	actor := deadlineGuardActor(t)
	opened, err := manager.Open(t.Context(), deadlineGuardOperation(actor, "backward-clock"), 80, 24, "xterm")
	if err != nil {
		t.Fatal(err)
	}
	wall.Add(time.Minute.Nanoseconds())
	_, _, _, _, advanced, err := manager.Read(t.Context(), opened.ID, actor, 0, 100)
	if err != nil || !advanced.LastActivityAt.Equal(createdAt.Add(time.Minute)) {
		t.Fatalf("forward activity did not advance: %+v err=%v", advanced, err)
	}
	wall.Add(-time.Hour.Nanoseconds())
	if _, err := manager.Write(t.Context(), opened.ID, actor, "synthetic", "clock test", "clock-write"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Control(t.Context(), opened.ID, actor, domain.ControlKeyCtrlC, "clock test", "clock-control"); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, observed, err := manager.Read(t.Context(), opened.ID, actor, 0, 100)
	if err != nil || observed.LastActivityAt.Before(advanced.LastActivityAt) {
		t.Fatalf("activity moved before creation: %+v err=%v", observed, err)
	}
	wall.Store(createdAt.Add(2 * time.Minute).UnixNano())
	_, _, _, _, forward, err := manager.Read(t.Context(), opened.ID, actor, 0, 100)
	if err != nil || !forward.LastActivityAt.Equal(createdAt.Add(2*time.Minute)) {
		t.Fatalf("clock recovery did not advance: %+v err=%v", forward, err)
	}
	wall.Store(createdAt.UnixNano())
	closed, err := manager.Close(t.Context(), opened.ID, actor, "clock test cleanup")
	if err != nil || closed.Validate() != nil || closed.ClosedAt.Before(forward.LastActivityAt) {
		t.Fatalf("clock step corrupted closure: %+v err=%v", closed, err)
	}
	loaded, err := sessions.NewManager(directory, nil, nil).Get(t.Context(), opened.ID, actor)
	if err != nil || loaded.State != domain.SessionStateClosed {
		t.Fatalf("clock step corrupted durable state: %+v err=%v", loaded, err)
	}
}

func TestReconcileCrashedSessionSurvivesBackwardClockStep(t *testing.T) {
	directory := testSessionsDir(t)
	obs := testSessionObservation("sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4", domain.SessionStateActive)
	obs.LastActivityAt = obs.CreatedAt.Add(time.Minute)
	writeSessionObservation(t, directory, obs)
	if _, err := sessions.ReconcileCrashedSessions(t.Context(), directory, obs.CreatedAt.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	loaded, err := sessions.NewManager(directory, nil, nil).Get(t.Context(), obs.ID, testSessionActor(t, obs.OwnerActor))
	if err != nil || loaded.Validate() != nil || loaded.ClosedAt.Before(obs.LastActivityAt) {
		t.Fatalf("restart clock step corrupted durable state: %+v err=%v", loaded, err)
	}
}
