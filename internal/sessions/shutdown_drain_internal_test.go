package sessions

import (
	"testing"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestManagerDrainedTracksEveryOwnedLifecycle(t *testing.T) {
	var nilManager *Manager
	if !nilManager.Drained() {
		t.Fatal("nil manager should have no owned lifecycle")
	}

	manager := &Manager{
		sessions:        make(map[domain.SessionID]*Session),
		pendingCleanups: make(map[uint64]*pendingCleanup),
	}
	if !manager.Drained() {
		t.Fatal("empty manager should be drained")
	}
	manager.activeOpens = 1
	if manager.Drained() {
		t.Fatal("active open must keep manager undrained")
	}
	manager.activeOpens = 0
	manager.pendingCleanups[1] = &pendingCleanup{}
	if manager.Drained() {
		t.Fatal("pending cleanup must keep manager undrained")
	}
	delete(manager.pendingCleanups, 1)
	session := &Session{obs: domain.SessionObservation{State: domain.SessionStateActive}}
	manager.sessions["sess-drain-truth"] = session
	if manager.Drained() {
		t.Fatal("active session must keep manager undrained")
	}
	session.obs.State = domain.SessionStateClosed
	if !manager.Drained() {
		t.Fatal("terminal session with no pending cleanup should be drained")
	}
}
