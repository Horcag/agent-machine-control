package sessions_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
	"github.com/Horcag/agent-machine-control/internal/sessions"
)

func TestManagerShutdownClosesOpenAdmissionBeforeSnapshot(t *testing.T) {
	transport := &deadlineGuardTransport{channel: newDeadlineGuardChannel()}
	mgr := sessions.NewManager(testSessionsDir(t), transport, time.Now)
	if err := mgr.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	actor := deadlineGuardActor(t)
	if _, err := mgr.Open(context.Background(), deadlineGuardOperation(actor, "after-shutdown"), 80, 24, "xterm"); !errors.Is(err, domain.ErrSessionManagerClosed) {
		t.Fatalf("Open after shutdown = %v, want ErrSessionManagerClosed", err)
	}
	if got := transport.dialCalls.Load(); got != 0 {
		t.Fatalf("post-cutover dial calls = %d", got)
	}
}
