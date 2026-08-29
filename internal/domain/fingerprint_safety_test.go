package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func validTestActor() domain.ActorContext {
	actor, _ := domain.NewActorContext(
		"user:alice",
		"user:alice",
		domain.NewScopeSet("machine:read", "machine:write", "machine:admin"),
		domain.NewScopeSet("machine:read", "machine:write"),
	)
	return actor
}

func TestFingerprint_SafePublicExecutionWithoutPriorValidation(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	actor := validTestActor()

	// 1. Incoherent delegation context (effective permissions exceed caller authority)
	badActor := domain.ActorContext{
		AuthenticatedCaller:  "user:alice",
		EffectiveActor:       "user:alice",
		CallerPermissions:    domain.NewScopeSet("machine:read"),
		EffectivePermissions: domain.NewScopeSet("machine:read", "machine:admin"),
	}
	opBadActor := domain.Operation{
		Kind:                "machine.start",
		Target:              "vm-alpha",
		Actor:               badActor,
		Reason:              "valid reason",
		Deadline:            now.Add(5 * time.Minute),
		IdempotencyKey:      "idemp-1",
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}
	if _, err := opBadActor.Fingerprint(); !errors.Is(err, domain.ErrDelegationExceedsAuthority) {
		t.Fatalf("expected ErrDelegationExceedsAuthority, got %v", err)
	}
	if _, err := domain.ComputeOperationFingerprint(opBadActor); !errors.Is(err, domain.ErrDelegationExceedsAuthority) {
		t.Fatalf("expected ComputeOperationFingerprint to return ErrDelegationExceedsAuthority, got %v", err)
	}

	// 2. Missing reason in mutation
	opMissingReason := domain.Operation{
		Kind:                "machine.start",
		Target:              "vm-alpha",
		Actor:               actor,
		Reason:              "",
		Deadline:            now.Add(5 * time.Minute),
		IdempotencyKey:      "idemp-1",
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}
	if _, err := opMissingReason.Fingerprint(); !errors.Is(err, domain.ErrMissingReason) {
		t.Fatalf("expected ErrMissingReason, got %v", err)
	}

	// 3. Missing idempotency key in mutation
	opMissingKey := domain.Operation{
		Kind:                "machine.start",
		Target:              "vm-alpha",
		Actor:               actor,
		Reason:              "valid reason",
		Deadline:            now.Add(5 * time.Minute),
		IdempotencyKey:      "",
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}
	if _, err := opMissingKey.Fingerprint(); !errors.Is(err, domain.ErrMissingIdempotencyKey) {
		t.Fatalf("expected ErrMissingIdempotencyKey, got %v", err)
	}
}

func TestFingerprint_RejectsLineOrientedPayloadAmbiguities(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	actor := validTestActor()

	baseMutation := domain.Operation{
		Kind:                "machine.start",
		Target:              "vm-alpha",
		Actor:               actor,
		Reason:              "starting machine for task",
		Deadline:            now.Add(5 * time.Minute),
		IdempotencyKey:      "idemp-123",
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	reasonCases := []struct {
		name   string
		reason string
	}{
		{"newline in reason", "line1\nline2"},
		{"carriage return in reason", "line1\rline2"},
		{"crlf in reason", "line1\r\nline2"},
		{"tab control char in reason", "line1\tline2"},
		{"null byte in reason", "line1\x00line2"},
		{"leading space in reason", " starting machine"},
		{"trailing space in reason", "starting machine "},
		{"whitespace only reason", "   "},
		{"overlong reason", strings.Repeat("r", domain.MaxReasonLength+1)},
		{"invalid utf8 reason", "reason\xff\xfe"},
	}

	for _, tt := range reasonCases {
		t.Run("reason_"+tt.name, func(t *testing.T) {
			op := baseMutation
			op.Reason = tt.reason
			_, err := op.Fingerprint()
			if err == nil {
				t.Fatalf("expected ErrInvalidReason for %s, got nil", tt.name)
			}
			if !errors.Is(err, domain.ErrInvalidReason) {
				t.Fatalf("expected ErrInvalidReason for %s, got %v", tt.name, err)
			}
			if errors.Is(err, domain.ErrMissingReason) {
				t.Fatalf("malformed present reason must not return ErrMissingReason")
			}
		})
	}

	keyCases := []struct {
		name string
		key  string
	}{
		{"newline in idempotency key", "key\nforged"},
		{"carriage return in idempotency key", "key\rforged"},
		{"crlf in idempotency key", "key\r\nforged"},
		{"tab in idempotency key", "key\t1"},
		{"null byte in idempotency key", "key\x001"},
		{"leading space in idempotency key", " key-1"},
		{"trailing space in idempotency key", "key-1 "},
		{"whitespace only key", "  "},
		{"overlong idempotency key", strings.Repeat("k", domain.MaxIdempotencyKeyLength+1)},
		{"invalid utf8 key", "key\xff\xfe"},
	}

	for _, tt := range keyCases {
		t.Run("key_"+tt.name, func(t *testing.T) {
			op := baseMutation
			op.IdempotencyKey = tt.key
			_, err := op.Fingerprint()
			if err == nil {
				t.Fatalf("expected ErrInvalidIdempotencyKey for %s, got nil", tt.name)
			}
			if !errors.Is(err, domain.ErrInvalidIdempotencyKey) {
				t.Fatalf("expected ErrInvalidIdempotencyKey for %s, got %v", tt.name, err)
			}
			if errors.Is(err, domain.ErrMissingIdempotencyKey) {
				t.Fatalf("malformed present key must not return ErrMissingIdempotencyKey")
			}
		})
	}
}

func TestFingerprint_ObserveOptionalFieldsContract(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	actor := validTestActor()

	baseObserve := domain.Operation{
		Kind:                "machine.inspect",
		Target:              "vm-alpha",
		Actor:               actor,
		Deadline:            now.Add(5 * time.Minute),
		Classification:      domain.ClassObserve,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	// Observe without reason and without key is valid
	fp1, err := baseObserve.Fingerprint()
	if err != nil {
		t.Fatalf("expected observe without reason/key to succeed, got %v", err)
	}
	if fp1 == "" {
		t.Fatalf("expected non-empty fingerprint")
	}

	// Observe with valid reason and valid key is valid
	opWithFields := baseObserve
	opWithFields.Reason = "routine monitoring check"
	opWithFields.IdempotencyKey = "obs-key-100"
	fp2, err := opWithFields.Fingerprint()
	if err != nil {
		t.Fatalf("expected observe with valid reason/key to succeed, got %v", err)
	}
	if fp1 == fp2 {
		t.Fatalf("expected fingerprints with and without optional fields to differ")
	}

	// Observe with malformed reason must be rejected with ErrInvalidReason
	opBadReason := baseObserve
	opBadReason.Reason = "invalid\nreason"
	if _, err := opBadReason.Fingerprint(); !errors.Is(err, domain.ErrInvalidReason) {
		t.Fatalf("expected ErrInvalidReason for malformed observe reason, got %v", err)
	}

	// Observe with malformed key must be rejected with ErrInvalidIdempotencyKey
	opBadKey := baseObserve
	opBadKey.IdempotencyKey = " bad key "
	if _, err := opBadKey.Fingerprint(); !errors.Is(err, domain.ErrInvalidIdempotencyKey) {
		t.Fatalf("expected ErrInvalidIdempotencyKey for malformed observe key, got %v", err)
	}
}
