package daemon_test

import (
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/daemon"
)

func TestResolveSessionDeadlineApprovalBinding(t *testing.T) {
	tests := []struct {
		name       string
		approvalID string
		raw        string
		want       time.Time
		wantErr    bool
	}{
		{name: "relative mutation omits exact deadline"},
		{name: "approval requires deadline", approvalID: "app-session-reference", wantErr: true},
		{name: "non UTC spelling rejected", approvalID: "app-session-reference", raw: "2026-08-30T14:00:00+04:00", wantErr: true},
		{
			name: "canonical UTC nanoseconds accepted", approvalID: "app-session-reference",
			raw:  "2026-08-30T10:00:00.123456789Z",
			want: time.Date(2026, 8, 30, 10, 0, 0, 123456789, time.UTC),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := daemon.ResolveSessionDeadline(test.approvalID, test.raw)
			if (err != nil) != test.wantErr || !got.Equal(test.want) {
				t.Fatalf("deadline=%s err=%v", got, err)
			}
		})
	}
}
