package sessions_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	guestssh "github.com/Horcag/agent-machine-control/internal/guest/ssh"
	"github.com/Horcag/agent-machine-control/internal/sessions"
)

var errSyntheticOpen = errors.New("synthetic post-effect open failure")

func openFailureOperation(t *testing.T) (domain.Operation, domain.ActorContext) {
	t.Helper()
	actor := lifecycleActor(t)
	return domain.Operation{
		Kind:           "session.open",
		Target:         "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		Actor:          actor,
		IdempotencyKey: "post-effect-open-failure",
	}, actor
}

func TestManagerPostEffectOpenFailuresCloseCompletelyWithoutPublication(t *testing.T) {
	tests := []struct {
		name string
		opts []sessions.ManagerOption
	}{
		{
			name: "session ID generation",
			opts: []sessions.ManagerOption{sessions.WithSessionIDGenerator(func() (domain.SessionID, error) {
				return "", errSyntheticOpen
			})},
		},
		{
			name: "session publication",
			opts: []sessions.ManagerOption{sessions.WithPublishOpenHook(func() error { return errSyntheticOpen })},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel := newLifecycleChannel(guestssh.CloseOutcome{Complete: true})
			mgr := sessions.NewManager(t.TempDir(), lifecycleTransport{channel: channel}, time.Now, tt.opts...)
			op, actor := openFailureOperation(t)

			obs, err := mgr.Open(context.Background(), op, 80, 24, domain.DefaultTermType)
			var failure *sessions.OpenFailure
			if obs != nil || !errors.As(err, &failure) || !errors.Is(err, errSyntheticOpen) {
				t.Fatalf("open failure = obs %+v err %v", obs, err)
			}
			if !failure.ChannelCreated || !failure.CleanupComplete || failure.CleanupErr != nil {
				t.Fatalf("effect truth = %+v, want created and completely cleaned", failure)
			}
			unwrapped := failure.Unwrap()
			if len(unwrapped) != 1 || unwrapped[0] == nil || !errors.Is(unwrapped[0], errSyntheticOpen) {
				t.Fatalf("complete cleanup unwrap = %v, want only non-nil open cause", unwrapped)
			}
			if got := channel.closeCalls.Load(); got != 1 {
				t.Fatalf("cleanup calls = %d, want 1", got)
			}
			listed, err := mgr.List(context.Background(), actor, "")
			if err != nil || len(listed) != 0 {
				t.Fatalf("published sessions = %+v err %v, want none", listed, err)
			}
		})
	}
}

func TestManagerPostEffectOpenCleanupUsesRemainingDeadline(t *testing.T) {
	channel := newLifecycleChannel()
	channel.allowClose = make(chan struct{})
	mgr := sessions.NewManager(
		t.TempDir(),
		lifecycleTransport{channel: channel},
		time.Now,
		sessions.WithSessionIDGenerator(func() (domain.SessionID, error) { return "", errSyntheticOpen }),
		sessions.WithCleanupTimeout(25*time.Millisecond),
	)
	op, _ := openFailureOperation(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	started := time.Now()
	obs, err := mgr.Open(ctx, op, 80, 24, domain.DefaultTermType)
	elapsed := time.Since(started)
	var failure *sessions.OpenFailure
	if obs != nil || !errors.As(err, &failure) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded cleanup = obs %+v err %v", obs, err)
	}
	if !failure.ChannelCreated || failure.CleanupComplete || !errors.Is(failure.CleanupErr, context.DeadlineExceeded) {
		t.Fatalf("effect truth = %+v, want incomplete deadline cleanup", failure)
	}
	unwrapped := failure.Unwrap()
	if len(unwrapped) != 2 || unwrapped[0] == nil || unwrapped[1] == nil || !errors.Is(err, errSyntheticOpen) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("incomplete cleanup unwrap = %v, want two non-nil causes", unwrapped)
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("bounded cleanup elapsed %s, want <= 250ms", elapsed)
	}
}

func TestManagerSupervisedOpenCleanupRemovesOwnershipAfterSuccess(t *testing.T) {
	channel := newLifecycleChannel(
		guestssh.CloseOutcome{Complete: false, Err: context.DeadlineExceeded},
		guestssh.CloseOutcome{Complete: true},
	)
	mgr := sessions.NewManager(
		t.TempDir(), lifecycleTransport{channel: channel}, time.Now,
		sessions.WithSessionIDGenerator(func() (domain.SessionID, error) { return "", errSyntheticOpen }),
	)
	op, actor := openFailureOperation(t)

	obs, err := mgr.Open(context.Background(), op, 80, 24, domain.DefaultTermType)
	var failure *sessions.OpenFailure
	if obs != nil || !errors.As(err, &failure) || failure.CleanupComplete {
		t.Fatalf("open = obs %+v err %v, want incomplete typed cleanup failure", obs, err)
	}
	waitForLifecycleCloseCalls(t, channel, 2)
	if err := mgr.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown after supervised cleanup = %v", err)
	}
	if got := channel.closeCalls.Load(); got != 2 {
		t.Fatalf("cleanup calls after shutdown = %d, want 2 with no retained orphan", got)
	}
	listed, listErr := mgr.List(context.Background(), actor, "")
	if listErr != nil || len(listed) != 0 {
		t.Fatalf("published sessions = %+v err %v, want none", listed, listErr)
	}
}

func TestManagerShutdownRetriesRetainedOpenCleanup(t *testing.T) {
	channel := newLifecycleChannel(
		guestssh.CloseOutcome{Complete: false, Err: context.DeadlineExceeded},
		guestssh.CloseOutcome{Complete: false, Err: context.DeadlineExceeded},
		guestssh.CloseOutcome{Complete: true},
	)
	mgr := sessions.NewManager(
		t.TempDir(), lifecycleTransport{channel: channel}, time.Now,
		sessions.WithSessionIDGenerator(func() (domain.SessionID, error) { return "", errSyntheticOpen }),
	)
	op, _ := openFailureOperation(t)
	if _, err := mgr.Open(context.Background(), op, 80, 24, domain.DefaultTermType); err == nil {
		t.Fatal("open unexpectedly succeeded")
	}
	waitForLifecycleCloseCalls(t, channel, 2)
	if err := mgr.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown retained cleanup = %v", err)
	}
	if got := channel.closeCalls.Load(); got != 3 {
		t.Fatalf("cleanup calls = %d, want request, supervisor, and shutdown attempts", got)
	}
	if err := mgr.Shutdown(context.Background()); err != nil {
		t.Fatalf("repeated shutdown after cleanup = %v", err)
	}
	if got := channel.closeCalls.Load(); got != 3 {
		t.Fatalf("repeated shutdown cleanup calls = %d, want retained cleanup removed", got)
	}
}

func TestManagerShutdownReturnsStableErrorForRetainedOpenCleanup(t *testing.T) {
	failed := guestssh.CloseOutcome{Complete: false, Err: context.DeadlineExceeded}
	channel := newLifecycleChannel(failed, failed, failed, failed)
	mgr := sessions.NewManager(
		t.TempDir(), lifecycleTransport{channel: channel}, time.Now,
		sessions.WithSessionIDGenerator(func() (domain.SessionID, error) { return "", errSyntheticOpen }),
	)
	op, _ := openFailureOperation(t)
	if _, err := mgr.Open(context.Background(), op, 80, 24, domain.DefaultTermType); err == nil {
		t.Fatal("open unexpectedly succeeded")
	}
	waitForLifecycleCloseCalls(t, channel, 2)
	firstErr := mgr.Shutdown(context.Background())
	secondErr := mgr.Shutdown(context.Background())
	if firstErr == nil || secondErr == nil || firstErr.Error() != secondErr.Error() {
		t.Fatalf("shutdown errors = %v and %v, want stable non-nil error", firstErr, secondErr)
	}
}

func waitForLifecycleCloseCalls(t *testing.T, channel *lifecycleChannel, want int32) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if channel.closeCalls.Load() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("cleanup calls = %d, want at least %d", channel.closeCalls.Load(), want)
}
