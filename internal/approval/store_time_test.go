package approval_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/approval"
	"github.com/Horcag/agent-machine-control/internal/domain"
)

func timeIdentityApproval() domain.Approval {
	issuedAt := time.Date(2026, 8, 30, 12, 0, 0, 238034000, time.UTC)
	return domain.Approval{
		ID: "app-time-identity", Actor: "agent:synthetic", Target: "a0b1c2d3-e4f5-6789-abcd-ef0123456789",
		AuthorizedClass: domain.ClassDestructivePrivileged,
		Fingerprint:     "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
		IdempotencyKey:  "time-identity-key", IssuedAt: issuedAt, ExpiresAt: issuedAt.Add(time.Minute),
	}
}

func TestStoreEquivalentTimeRepresentationsPreserveApprovalLifecycle(t *testing.T) {
	for _, zone := range []string{"Z", "+00:00", "+04:00"} {
		t.Run(zone, func(t *testing.T) {
			issued := timeIdentityApproval()
			candidate := issued.Clone()
			// A named fixed zone makes this independent of the host's local timezone.
			location := time.UTC
			if zone != "Z" {
				offset := 0
				if zone == "+04:00" {
					offset = 4 * 60 * 60
				}
				location = time.FixedZone(zone, offset)
			}
			candidate.IssuedAt = candidate.IssuedAt.In(location)
			candidate.ExpiresAt = candidate.ExpiresAt.In(location)
			testApprovalLifecycle(t, candidate)
		})
	}
}

func testApprovalLifecycle(t *testing.T, candidate domain.Approval) {
	t.Helper()
	store := approval.NewStore(t.TempDir())
	if err := store.Issue(candidate); err != nil {
		t.Fatal(err)
	}
	// Reopen to prove comparison against serialized UTC provenance.
	loaded, err := store.LoadIssuedContext(context.Background(), string(candidate.ID))
	if err != nil || loaded == nil || loaded.IssuedAt.Location() != time.UTC {
		t.Fatalf("persisted issuance = %+v, error = %v", loaded, err)
	}
	if err := store.ValidateIssuedContext(context.Background(), candidate); err != nil {
		t.Fatalf("equal-instant validation: %v", err)
	}
	consumedAt := candidate.IssuedAt.Add(time.Second)
	if err := store.MarkConsumed(candidate, consumedAt); err != nil {
		t.Fatalf("equal-instant consumption: %v", err)
	}
	if err := store.MarkConsumed(candidate, consumedAt); !errors.Is(err, domain.ErrApprovalConsumed) {
		t.Fatalf("replay = %v, want ErrApprovalConsumed", err)
	}
	if err := store.ReleaseUnexecutedContext(context.Background(), candidate); err != nil {
		t.Fatalf("equal-instant release: %v", err)
	}
	if consumed, err := store.IsConsumed(string(candidate.ID)); err != nil || consumed {
		t.Fatalf("released consumption = %v, error = %v", consumed, err)
	}
	if err := store.ValidateIssuedContext(context.Background(), candidate); err != nil {
		t.Fatalf("release removed issuance: %v", err)
	}
	if err := store.MarkConsumed(candidate, consumedAt); err != nil {
		t.Fatalf("released authority cannot be consumed: %v", err)
	}
}

func TestStoreTimeIdentityDoesNotAcceptChangedAuthority(t *testing.T) {
	mutations := map[string]func(*domain.Approval){
		"id":     func(a *domain.Approval) { a.ID = "app-other-identity" },
		"actor":  func(a *domain.Approval) { a.Actor = "agent:other" },
		"target": func(a *domain.Approval) { a.Target = "b0b1c2d3-e4f5-6789-abcd-ef0123456789" },
		"class":  func(a *domain.Approval) { a.AuthorizedClass = domain.ClassObserve },
		"fingerprint": func(a *domain.Approval) {
			a.Fingerprint = "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
		},
		"key":            func(a *domain.Approval) { a.IdempotencyKey = "other-key" },
		"issued instant": func(a *domain.Approval) { a.IssuedAt = a.IssuedAt.Add(time.Nanosecond) },
		"expiry instant": func(a *domain.Approval) { a.ExpiresAt = a.ExpiresAt.Add(time.Nanosecond) },
		"consumed flag":  func(a *domain.Approval) { a.Consumed = true },
		"consumed time":  func(a *domain.Approval) { at := a.IssuedAt.Add(time.Second); a.ConsumedAt = &at },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			testChangedApprovalAuthority(t, name, mutate)
		})
	}
}

func testChangedApprovalAuthority(t *testing.T, name string, mutate func(*domain.Approval)) {
	t.Helper()
	issued := timeIdentityApproval()
	store := approval.NewStore(t.TempDir())
	if err := store.Issue(issued); err != nil {
		t.Fatal(err)
	}
	candidate := issued.Clone()
	candidate.ExpiresAt = candidate.ExpiresAt.In(time.FixedZone("+00:00", 0))
	mutate(&candidate)
	if err := store.ValidateIssuedContext(context.Background(), candidate); !errors.Is(err, approval.ErrApprovalNotIssued) {
		t.Fatalf("changed authority validation = %v", err)
	}
	if err := store.MarkConsumed(candidate, issued.IssuedAt.Add(time.Second)); !errors.Is(err, approval.ErrApprovalNotIssued) {
		t.Fatalf("changed authority consumption = %v", err)
	}
	if consumed, err := store.IsConsumed(string(issued.ID)); err != nil || consumed {
		t.Fatalf("rejected candidate wrote consumption: %v, %v", consumed, err)
	}
	if err := store.MarkConsumed(issued, issued.IssuedAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	err := store.ReleaseUnexecutedContext(context.Background(), candidate)
	// A missing ID remains an idempotent no-op, never releasing another ID.
	if name != "id" && !errors.Is(err, approval.ErrApprovalNotIssued) {
		t.Fatalf("changed authority release = %v", err)
	}
	if consumed, err := store.IsConsumed(string(issued.ID)); err != nil || !consumed {
		t.Fatalf("changed authority released consumption: %v, %v", consumed, err)
	}
}
