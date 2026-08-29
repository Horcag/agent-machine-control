package receipt

import (
	"errors"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestMatchReceipt(t *testing.T) {
	deadline := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	op := domain.Operation{
		Kind:                domain.OperationKind("machine.start"),
		Target:              domain.MachineRef("vm-alpha"),
		Actor:               domain.ActorContext{AuthenticatedCaller: "user:alice", EffectiveActor: "user:alice"},
		Reason:              "testing",
		Deadline:            deadline,
		IdempotencyKey:      "idem-1",
		Classification:      domain.ClassReversibleMutation,
		EvidenceSensitivity: domain.EvidenceSensitivityStandard,
	}

	fp, err := domain.ComputeOperationFingerprint(op)
	requireNoError(t, err, "unexpected error computing operation fingerprint")
	idFp, err := domain.ComputeIdempotencyFingerprint(op)
	requireNoError(t, err, "unexpected error computing idempotency fingerprint")

	rcpt := domain.Receipt{
		IdempotencyKey:         "idem-1",
		Fingerprint:            fp,
		IdempotencyFingerprint: idFp,
		Actor:                  "user:alice",
		Target:                 "vm-alpha",
	}

	// 1. Exact match
	matched, err := matchReceipt(rcpt, op, fp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !matched {
		t.Fatalf("expected match")
	}

	// 2. IdempotencyKey mismatch
	opMismatch := op
	opMismatch.IdempotencyKey = "idem-2"
	matched, err = matchReceipt(rcpt, opMismatch, fp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if matched {
		t.Fatalf("expected mismatch")
	}

	// 3. Legacy fallback (IdempotencyFingerprint == "") - match
	rcptLegacy := rcpt
	rcptLegacy.IdempotencyFingerprint = ""
	matched, err = matchReceipt(rcptLegacy, op, fp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !matched {
		t.Fatalf("expected match on legacy fallback")
	}

	// 4. Legacy fallback (IdempotencyFingerprint == "") - mismatch/collision
	matched, err = matchReceipt(rcptLegacy, op, "sha256:different")
	if !errors.Is(err, ErrIdempotencyCollision) {
		t.Fatalf("expected ErrIdempotencyCollision, got err=%v", err)
	}
	if matched {
		t.Fatalf("expected false")
	}

	// 5. IdempotencyFingerprint mismatch/collision
	rcptCollision := rcpt
	rcptCollision.IdempotencyFingerprint = "sha256:different"
	matched, err = matchReceipt(rcptCollision, op, fp)
	if !errors.Is(err, ErrIdempotencyCollision) {
		t.Fatalf("expected ErrIdempotencyCollision, got err=%v", err)
	}
	if matched {
		t.Fatalf("expected false")
	}

	// 6. Actor mismatch
	rcptActorMismatch := rcpt
	rcptActorMismatch.Actor = "user:bob"
	_, err = matchReceipt(rcptActorMismatch, op, fp)
	if !errors.Is(err, ErrIdempotencyCollision) {
		t.Fatalf("expected ErrIdempotencyCollision, got err=%v", err)
	}

	// 7. Target mismatch
	rcptTargetMismatch := rcpt
	rcptTargetMismatch.Target = "vm-beta"
	_, err = matchReceipt(rcptTargetMismatch, op, fp)
	if !errors.Is(err, ErrIdempotencyCollision) {
		t.Fatalf("expected ErrIdempotencyCollision, got err=%v", err)
	}

	// 8. ComputeIdempotencyFingerprint error
	opInvalid := op
	opInvalid.Actor.AuthenticatedCaller = "" // Invalid actor -> causes ComputeIdempotencyFingerprint to error
	matched, err = matchReceipt(rcpt, opInvalid, fp)
	if err == nil {
		t.Fatalf("expected error from invalid operation fingerprinting")
	}
	if matched {
		t.Fatalf("expected false")
	}
}

func requireNoError(t *testing.T, err error, msg string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", msg, err)
	}
}
