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
	if elapsed > 250*time.Millisecond {
		t.Fatalf("bounded cleanup elapsed %s, want <= 250ms", elapsed)
	}
}
