package approval_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestConsumptionRequiresActiveTimeAndPersistsValidRecord(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		at   time.Time
		want error
	}{
		{"before issuance", now.Add(-time.Nanosecond), domain.ErrApprovalNotYetValid},
		{"at expiry", now.Add(time.Minute), domain.ErrApprovalExpired},
		{"after expiry", now.Add(2 * time.Minute), domain.ErrApprovalExpired},
		{"at issuance", now, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			store := approval.NewStore(directory)
			issued := domain.Approval{ID: "app-consumption-time", Actor: "operator:synthetic", Target: "11111111-1111-4111-8111-111111111111", AuthorizedClass: domain.ClassDestructivePrivileged, Fingerprint: "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef", IdempotencyKey: "consumption-time", IssuedAt: now, ExpiresAt: now.Add(time.Minute)}
			if err := store.Issue(issued); err != nil {
				t.Fatal(err)
			}
			if err := store.MarkConsumed(issued, tc.at); !errors.Is(err, tc.want) {
				t.Fatalf("consumption error=%v, want %v", err, tc.want)
			}
			consumed, err := store.IsConsumed(string(issued.ID))
			if err != nil || consumed != (tc.want == nil) {
				t.Fatalf("consumption state=%v err=%v", consumed, err)
			}
			if tc.want == nil {
				loaded, err := approval.LoadFromFile(filepath.Join(directory, string(issued.ID)+".json"))
				if err != nil || !loaded.Consumed || !loaded.ConsumedAt.Equal(tc.at) {
					t.Fatalf("invalid durable consumption=%+v err=%v", loaded, err)
				}
			}
		})
	}
}
