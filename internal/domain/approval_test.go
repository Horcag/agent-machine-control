package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func newSampleOperation(now time.Time) (domain.Operation, domain.Fingerprint) {
	actorCtx, _ := domain.NewActorContext("user:admin", "user:admin", domain.NewScopeSet("machine:admin"), domain.NewScopeSet("machine:admin"))
	op := domain.Operation{
		Kind:                "machine.delete",
		Target:              "vm-target",
		Actor:               actorCtx,
		Reason:              "decommissioning vm",
		Deadline:            now.Add(10 * time.Minute),
		IdempotencyKey:      "idemp-del-1",
		RequiredCapability:  "hyperv.lifecycle",
		RequiredScopes:      []string{"machine:admin"},
		Classification:      domain.ClassDestructivePrivileged,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
		Parameters:          map[string]any{"force": true},
	}
	fp, _ := op.Fingerprint()
	return op, fp
}

func TestApproval_ValidateAndMatches(t *testing.T) {
	now := time.Date(2026, 8, 29, 14, 0, 0, 0, time.UTC)
	op, fp := newSampleOperation(now)

	validApproval := domain.Approval{
		ID:              "app-1001",
		Actor:           "user:admin",
		Target:          "vm-target",
		AuthorizedClass: domain.ClassDestructivePrivileged,
		Fingerprint:     fp,
		IdempotencyKey:  "idemp-del-1",
		IssuedAt:        now.Add(-1 * time.Minute),
		ExpiresAt:       now.Add(5 * time.Minute),
		Consumed:        false,
	}

	if validApproval.ID.String() != "app-1001" {
		t.Errorf("ApprovalID.String() = %q, want app-1001", validApproval.ID.String())
	}
	if err := validApproval.Validate(); err != nil {
		t.Fatalf("expected valid approval, got error: %v", err)
	}
	if err := validApproval.Matches(op); err != nil {
		t.Fatalf("expected approval to match operation, got error: %v", err)
	}
	if err := validApproval.MatchesEffectiveClass(domain.ClassDestructivePrivileged); err != nil {
		t.Fatalf("expected approval to match effective class, got error: %v", err)
	}
	if err := validApproval.IsActive(now); err != nil {
		t.Fatalf("expected approval to be active, got error: %v", err)
	}
	if err := validApproval.ValidateForOperation(op, now); err != nil {
		t.Fatalf("expected ValidateForOperation to succeed, got %v", err)
	}
}

func TestApproval_Mismatches(t *testing.T) {
	now := time.Date(2026, 8, 29, 14, 0, 0, 0, time.UTC)
	op, fp := newSampleOperation(now)

	tests := []struct {
		name      string
		approval  domain.Approval
		wantErrIs error
	}{
		{
			name: "mismatched actor",
			approval: domain.Approval{
				ID:              "app-1",
				Actor:           "user:other",
				Target:          "vm-target",
				AuthorizedClass: domain.ClassDestructivePrivileged,
				Fingerprint:     fp,
				IdempotencyKey:  "idemp-del-1",
				IssuedAt:        now.Add(-1 * time.Minute),
				ExpiresAt:       now.Add(5 * time.Minute),
			},
			wantErrIs: domain.ErrApprovalActorMismatch,
		},
		{
			name: "mismatched target",
			approval: domain.Approval{
				ID:              "app-1",
				Actor:           "user:admin",
				Target:          "vm-other",
				AuthorizedClass: domain.ClassDestructivePrivileged,
				Fingerprint:     fp,
				IdempotencyKey:  "idemp-del-1",
				IssuedAt:        now.Add(-1 * time.Minute),
				ExpiresAt:       now.Add(5 * time.Minute),
			},
			wantErrIs: domain.ErrApprovalTargetMismatch,
		},
		{
			name: "mismatched idempotency key",
			approval: domain.Approval{
				ID:              "app-1",
				Actor:           "user:admin",
				Target:          "vm-target",
				AuthorizedClass: domain.ClassDestructivePrivileged,
				Fingerprint:     fp,
				IdempotencyKey:  "different-key",
				IssuedAt:        now.Add(-1 * time.Minute),
				ExpiresAt:       now.Add(5 * time.Minute),
			},
			wantErrIs: domain.ErrApprovalKeyMismatch,
		},
		{
			name: "mismatched fingerprint",
			approval: domain.Approval{
				ID:              "app-1",
				Actor:           "user:admin",
				Target:          "vm-target",
				AuthorizedClass: domain.ClassDestructivePrivileged,
				Fingerprint:     "sha256:0000000000000000000000000000000000000000000000000000000000000000",
				IdempotencyKey:  "idemp-del-1",
				IssuedAt:        now.Add(-1 * time.Minute),
				ExpiresAt:       now.Add(5 * time.Minute),
			},
			wantErrIs: domain.ErrApprovalFingerprintMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.approval.ValidateForOperation(op, now)
			if !errors.Is(err, tt.wantErrIs) {
				t.Fatalf("expected error %v, got %v", tt.wantErrIs, err)
			}
		})
	}

	t.Run("mismatched effective class", func(t *testing.T) {
		app := domain.Approval{
			ID:              "app-1",
			Actor:           "user:admin",
			Target:          "vm-target",
			AuthorizedClass: domain.ClassDestructivePrivileged,
			Fingerprint:     fp,
			IdempotencyKey:  "idemp-del-1",
			IssuedAt:        now.Add(-1 * time.Minute),
			ExpiresAt:       now.Add(5 * time.Minute),
		}
		if err := app.MatchesEffectiveClass(domain.ClassObserve); !errors.Is(err, domain.ErrApprovalClassMismatch) {
			t.Errorf("expected ErrApprovalClassMismatch, got %v", err)
		}
		if err := app.ValidateForEffectiveOperation(op, domain.ClassObserve, now); !errors.Is(err, domain.ErrApprovalClassMismatch) {
			t.Errorf("expected ErrApprovalClassMismatch in ValidateForEffectiveOperation, got %v", err)
		}
	})
}

func TestApproval_ActivityLifecycle(t *testing.T) {
	now := time.Date(2026, 8, 29, 14, 0, 0, 0, time.UTC)
	op, fp := newSampleOperation(now)
	consumedTime := now.Add(-30 * time.Second)

	tests := []struct {
		name      string
		approval  domain.Approval
		checkTime time.Time
		wantErrIs error
	}{
		{
			name: "expired approval",
			approval: domain.Approval{
				ID:              "app-1",
				Actor:           "user:admin",
				Target:          "vm-target",
				AuthorizedClass: domain.ClassDestructivePrivileged,
				Fingerprint:     fp,
				IdempotencyKey:  "idemp-del-1",
				IssuedAt:        now.Add(-10 * time.Minute),
				ExpiresAt:       now.Add(-1 * time.Minute),
			},
			checkTime: now,
			wantErrIs: domain.ErrApprovalExpired,
		},
		{
			name: "not yet valid approval",
			approval: domain.Approval{
				ID:              "app-1",
				Actor:           "user:admin",
				Target:          "vm-target",
				AuthorizedClass: domain.ClassDestructivePrivileged,
				Fingerprint:     fp,
				IdempotencyKey:  "idemp-del-1",
				IssuedAt:        now.Add(1 * time.Minute),
				ExpiresAt:       now.Add(10 * time.Minute),
			},
			checkTime: now,
			wantErrIs: domain.ErrApprovalNotYetValid,
		},
		{
			name: "already consumed approval",
			approval: domain.Approval{
				ID:              "app-1",
				Actor:           "user:admin",
				Target:          "vm-target",
				AuthorizedClass: domain.ClassDestructivePrivileged,
				Fingerprint:     fp,
				IdempotencyKey:  "idemp-del-1",
				IssuedAt:        now.Add(-5 * time.Minute),
				ExpiresAt:       now.Add(5 * time.Minute),
				Consumed:        true,
				ConsumedAt:      &consumedTime,
			},
			checkTime: now,
			wantErrIs: domain.ErrApprovalConsumed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.approval.ValidateForOperation(op, tt.checkTime)
			if !errors.Is(err, tt.wantErrIs) {
				t.Fatalf("expected error %v, got %v", tt.wantErrIs, err)
			}
		})
	}
}

func TestApproval_ConsumptionLifecycleConsistency(t *testing.T) {
	now := time.Date(2026, 8, 29, 14, 0, 0, 0, time.UTC)
	issued := now.Add(-10 * time.Minute)
	expires := now.Add(10 * time.Minute)
	consumedBeforeIssued := issued.Add(-1 * time.Minute)
	consumedAfterExpires := expires.Add(1 * time.Minute)
	consumedFuture := now.Add(5 * time.Minute)
	_, fp := newSampleOperation(now)

	// ConsumedAt set when Consumed is false
	unconsumedWithTime := domain.Approval{
		ID:              "app-1",
		Actor:           "user:admin",
		Target:          "vm-target",
		AuthorizedClass: domain.ClassDestructivePrivileged,
		Fingerprint:     fp,
		IdempotencyKey:  "idemp-del-1",
		IssuedAt:        issued,
		ExpiresAt:       expires,
		Consumed:        false,
		ConsumedAt:      &now,
	}
	if err := unconsumedWithTime.Validate(); err == nil || !errors.Is(err, domain.ErrInvalidApprovalRecord) {
		t.Errorf("expected ErrInvalidApprovalRecord for unconsumed approval with ConsumedAt, got %v", err)
	}

	// Consumed is true but ConsumedAt is nil
	consumedNil := domain.Approval{
		ID:              "app-1",
		Actor:           "user:admin",
		Target:          "vm-target",
		AuthorizedClass: domain.ClassDestructivePrivileged,
		Fingerprint:     fp,
		IdempotencyKey:  "idemp-del-1",
		IssuedAt:        issued,
		ExpiresAt:       expires,
		Consumed:        true,
		ConsumedAt:      nil,
	}
	if err := consumedNil.Validate(); err == nil || !errors.Is(err, domain.ErrInvalidApprovalRecord) {
		t.Errorf("expected ErrInvalidApprovalRecord for consumed approval with nil ConsumedAt, got %v", err)
	}

	// ConsumedAt before IssuedAt
	consumedPreIssued := domain.Approval{
		ID:              "app-1",
		Actor:           "user:admin",
		Target:          "vm-target",
		AuthorizedClass: domain.ClassDestructivePrivileged,
		Fingerprint:     fp,
		IdempotencyKey:  "idemp-del-1",
		IssuedAt:        issued,
		ExpiresAt:       expires,
		Consumed:        true,
		ConsumedAt:      &consumedBeforeIssued,
	}
	if err := consumedPreIssued.Validate(); err == nil || !errors.Is(err, domain.ErrInvalidApprovalRecord) {
		t.Errorf("expected ErrInvalidApprovalRecord for consumed_at before issued_at, got %v", err)
	}

	// ConsumedAt after ExpiresAt
	consumedPostExpires := domain.Approval{
		ID:              "app-1",
		Actor:           "user:admin",
		Target:          "vm-target",
		AuthorizedClass: domain.ClassDestructivePrivileged,
		Fingerprint:     fp,
		IdempotencyKey:  "idemp-del-1",
		IssuedAt:        issued,
		ExpiresAt:       expires,
		Consumed:        true,
		ConsumedAt:      &consumedAfterExpires,
	}
	if err := consumedPostExpires.Validate(); err == nil || !errors.Is(err, domain.ErrInvalidApprovalRecord) {
		t.Errorf("expected ErrInvalidApprovalRecord for consumed_at after expires_at, got %v", err)
	}

	// ConsumedAt in future relative to evaluation time
	consumedInFuture := domain.Approval{
		ID:              "app-1",
		Actor:           "user:admin",
		Target:          "vm-target",
		AuthorizedClass: domain.ClassDestructivePrivileged,
		Fingerprint:     fp,
		IdempotencyKey:  "idemp-del-1",
		IssuedAt:        issued,
		ExpiresAt:       expires,
		Consumed:        true,
		ConsumedAt:      &consumedFuture,
	}
	if err := consumedInFuture.Validate(); err != nil {
		t.Fatalf("expected structural validation to pass: %v", err)
	}
	if err := consumedInFuture.IsActive(now); err == nil || !errors.Is(err, domain.ErrInvalidApprovalRecord) {
		t.Errorf("expected ErrInvalidApprovalRecord when consumed_at is in future relative to evaluation time, got %v", err)
	}
}

func TestApproval_RecordStructuralValidation(t *testing.T) {
	now := time.Date(2026, 8, 29, 14, 0, 0, 0, time.UTC)
	_, fp := newSampleOperation(now)

	invalidApprovals := []domain.Approval{
		{ID: "", Actor: "user:admin", Target: "vm-target", AuthorizedClass: domain.ClassDestructivePrivileged, Fingerprint: fp, IdempotencyKey: "k", IssuedAt: now, ExpiresAt: now.Add(time.Minute)},
		{ID: " app-1", Actor: "user:admin", Target: "vm-target", AuthorizedClass: domain.ClassDestructivePrivileged, Fingerprint: fp, IdempotencyKey: "k", IssuedAt: now, ExpiresAt: now.Add(time.Minute)},
		{ID: "app-1", Actor: "", Target: "vm-target", AuthorizedClass: domain.ClassDestructivePrivileged, Fingerprint: fp, IdempotencyKey: "k", IssuedAt: now, ExpiresAt: now.Add(time.Minute)},
		{ID: "app-1", Actor: "user:admin", Target: "", AuthorizedClass: domain.ClassDestructivePrivileged, Fingerprint: fp, IdempotencyKey: "k", IssuedAt: now, ExpiresAt: now.Add(time.Minute)},
		{ID: "app-1", Actor: "user:admin", Target: "vm-target", AuthorizedClass: "", Fingerprint: fp, IdempotencyKey: "k", IssuedAt: now, ExpiresAt: now.Add(time.Minute)},
		{ID: "app-1", Actor: "user:admin", Target: "vm-target", AuthorizedClass: domain.ClassObserve, Fingerprint: fp, IdempotencyKey: "k", IssuedAt: now, ExpiresAt: now.Add(time.Minute)},
		{ID: "app-1", Actor: "user:admin", Target: "vm-target", AuthorizedClass: domain.ClassReversibleMutation, Fingerprint: fp, IdempotencyKey: "k", IssuedAt: now, ExpiresAt: now.Add(time.Minute)},
		{ID: "app-1", Actor: "user:admin", Target: "vm-target", AuthorizedClass: domain.ClassForbidden, Fingerprint: fp, IdempotencyKey: "k", IssuedAt: now, ExpiresAt: now.Add(time.Minute)},
		{ID: "app-1", Actor: "user:admin", Target: "vm-target", AuthorizedClass: "invalid_class", Fingerprint: fp, IdempotencyKey: "k", IssuedAt: now, ExpiresAt: now.Add(time.Minute)},
		{ID: "app-1", Actor: "user:admin", Target: "vm-target", AuthorizedClass: domain.ClassDestructivePrivileged, Fingerprint: "bad", IdempotencyKey: "k", IssuedAt: now, ExpiresAt: now.Add(time.Minute)},
		{ID: "app-1", Actor: "user:admin", Target: "vm-target", AuthorizedClass: domain.ClassDestructivePrivileged, Fingerprint: fp, IdempotencyKey: "", IssuedAt: now, ExpiresAt: now.Add(time.Minute)},
		{ID: "app-1", Actor: "user:admin", Target: "vm-target", AuthorizedClass: domain.ClassDestructivePrivileged, Fingerprint: fp, IdempotencyKey: " k", IssuedAt: now, ExpiresAt: now.Add(time.Minute)},
		{ID: "app-1", Actor: "user:admin", Target: "vm-target", AuthorizedClass: domain.ClassDestructivePrivileged, Fingerprint: fp, IdempotencyKey: "k"},
		{ID: "app-1", Actor: "user:admin", Target: "vm-target", AuthorizedClass: domain.ClassDestructivePrivileged, Fingerprint: fp, IdempotencyKey: "k", IssuedAt: now, ExpiresAt: now.Add(-time.Minute)},
	}

	for _, app := range invalidApprovals {
		if err := app.Validate(); err == nil || !errors.Is(err, domain.ErrInvalidApprovalRecord) {
			t.Errorf("expected ErrInvalidApprovalRecord for %v, got %v", app, err)
		}
	}
}
