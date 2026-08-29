package domain_test

import (
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestComputeFingerprint_CanonicalScopeCollisionRegression(t *testing.T) {
	deadline := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	caller := domain.ActorID("user:alice")
	effective := domain.ActorID("user:alice")
	target := domain.MachineRef("vm-alpha")
	kind := domain.OperationKind("machine.inspect")
	class := domain.ClassObserve
	reason := "inspection"
	key := ""
	reqCap := ""
	sens := domain.EvidenceSensitivityStandard
	params := map[string]any{"detail": "summary"}

	// Regression test for finding 1:
	// Previously, strings.Join(scopes, ",") caused ['a,b', 'c'] to collide with ['a', 'b,c'].
	scopes1 := []string{"a,b", "c"}
	scopes2 := []string{"a", "b,c"}

	fp1, err := domain.ComputeFingerprint(
		caller, effective, target, kind, class, reason, deadline, key, reqCap, scopes1, sens, params,
	)
	if err != nil {
		t.Fatalf("failed to compute fp1: %v", err)
	}

	fp2, err := domain.ComputeFingerprint(
		caller, effective, target, kind, class, reason, deadline, key, reqCap, scopes2, sens, params,
	)
	if err != nil {
		t.Fatalf("failed to compute fp2: %v", err)
	}

	if fp1 == fp2 {
		t.Fatalf("scope collision detected: scope sets %v and %v produced identical fingerprint %s", scopes1, scopes2, fp1)
	}

	// Ordering invariance and deduplication equivalence
	scopesOrdered := []string{"a", "b", "c"}
	scopesUnorderedWithDuplicates := []string{"c", "a", "b", "a", "c", "b"}

	fpOrdered, err := domain.ComputeFingerprint(
		caller, effective, target, kind, class, reason, deadline, key, reqCap, scopesOrdered, sens, params,
	)
	if err != nil {
		t.Fatalf("failed to compute fpOrdered: %v", err)
	}

	fpUnordered, err := domain.ComputeFingerprint(
		caller, effective, target, kind, class, reason, deadline, key, reqCap, scopesUnorderedWithDuplicates, sens, params,
	)
	if err != nil {
		t.Fatalf("failed to compute fpUnordered: %v", err)
	}

	if fpOrdered != fpUnordered {
		t.Fatalf("expected ordered and unordered/duplicate scope sets to produce identical fingerprint, got %s vs %s", fpOrdered, fpUnordered)
	}

	// Empty slice vs nil slice canonical equivalence
	fpNilScopes, err := domain.ComputeFingerprint(
		caller, effective, target, kind, class, reason, deadline, key, reqCap, nil, sens, params,
	)
	if err != nil {
		t.Fatalf("failed to compute fpNilScopes: %v", err)
	}

	fpEmptyScopes, err := domain.ComputeFingerprint(
		caller, effective, target, kind, class, reason, deadline, key, reqCap, []string{}, sens, params,
	)
	if err != nil {
		t.Fatalf("failed to compute fpEmptyScopes: %v", err)
	}

	if fpNilScopes != fpEmptyScopes {
		t.Fatalf("expected nil and empty scope slice to produce identical fingerprint, got %s vs %s", fpNilScopes, fpEmptyScopes)
	}
}
