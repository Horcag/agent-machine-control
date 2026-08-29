package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Fingerprint represents a cryptographic SHA-256 digest binding an operation's canonical payload.
type Fingerprint string

// String returns the string representation of the fingerprint.
func (f Fingerprint) String() string {
	return string(f)
}

// Validate checks that the fingerprint is in the standard format "sha256:<64-hex-digits>".
func (f Fingerprint) Validate() error {
	s := string(f)
	if !strings.HasPrefix(s, "sha256:") {
		return fmt.Errorf("%w: missing 'sha256:' prefix", ErrInvalidFingerprint)
	}
	hexPart := strings.TrimPrefix(s, "sha256:")
	if len(hexPart) != 64 {
		return fmt.Errorf("%w: invalid hex length %d (expected 64)", ErrInvalidFingerprint, len(hexPart))
	}
	for _, r := range hexPart {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return fmt.Errorf("%w: invalid hex character", ErrInvalidFingerprint)
		}
	}
	return nil
}

func validateOperationHeader(op Operation) error {
	if err := op.Actor.Validate(); err != nil {
		return fmt.Errorf("actor validation failed: %w", err)
	}
	if err := op.Target.Validate(); err != nil {
		return fmt.Errorf("target validation failed: %w", err)
	}
	if err := op.Kind.Validate(); err != nil {
		return fmt.Errorf("kind validation failed: %w", err)
	}
	if !op.Classification.IsValid() {
		return fmt.Errorf("%w: invalid classification", ErrInvalidOperationClass)
	}
	if !op.EvidenceSensitivity.IsValid() {
		return fmt.Errorf("%w: invalid evidence sensitivity", ErrInvalidEvidenceSensitivity)
	}
	if op.Deadline.IsZero() {
		return ErrMissingDeadline
	}
	if op.RequiredCapability != "" {
		return ValidateCapability(op.RequiredCapability)
	}
	return nil
}

func validateOperationReasonAndKey(op Operation) error {
	if op.Classification.IsMutation() {
		if err := ValidateIdempotencyKey(op.IdempotencyKey); err != nil {
			return err
		}
		return ValidateReason(op.Reason)
	}
	if op.Reason != "" {
		if err := ValidateReason(op.Reason); err != nil {
			return err
		}
	}
	if op.IdempotencyKey != "" {
		if err := ValidateIdempotencyKey(op.IdempotencyKey); err != nil {
			return err
		}
	}
	return nil
}

func validateOperationForFingerprint(op Operation) error {
	if err := validateOperationHeader(op); err != nil {
		return err
	}
	return validateOperationReasonAndKey(op)
}

func canonicalizeScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(scopes))
	var sortedScopes []string
	for _, sc := range scopes {
		if err := ValidateScope(sc); err != nil {
			return nil, err
		}
		if _, ok := seen[sc]; !ok {
			seen[sc] = struct{}{}
			sortedScopes = append(sortedScopes, sc)
		}
	}
	sort.Strings(sortedScopes)
	return sortedScopes, nil
}

// ComputeOperationFingerprint calculates the deterministic SHA-256 fingerprint for an operation,
// binding every security-relevant field.
func ComputeOperationFingerprint(op Operation) (Fingerprint, error) {
	return computeFingerprintInternal(op, true)
}

func computeFingerprintInternal(op Operation, includeDeadline bool) (Fingerprint, error) {
	if err := validateOperationForFingerprint(op); err != nil {
		return "", err
	}

	sortedScopes, err := canonicalizeScopes(op.RequiredScopes)
	if err != nil {
		return "", err
	}

	canonicalParams, err := CanonicalizeParameters(op.Parameters)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	if includeDeadline {
		buf.WriteString("AMC_OP_V2\n")
	} else {
		buf.WriteString("AMC_IDEMP_V1\n")
	}
	fmt.Fprintf(&buf, "CALLER:%s\n", op.Actor.AuthenticatedCaller)
	fmt.Fprintf(&buf, "ACTOR:%s\n", op.Actor.EffectiveActor)
	fmt.Fprintf(&buf, "TARGET:%s\n", op.Target)
	fmt.Fprintf(&buf, "KIND:%s\n", op.Kind)
	fmt.Fprintf(&buf, "CLASS:%s\n", op.Classification)
	fmt.Fprintf(&buf, "REASON:%s\n", op.Reason)
	if includeDeadline {
		fmt.Fprintf(&buf, "DEADLINE:%s\n", op.Deadline.UTC().Format(time.RFC3339Nano))
	}
	fmt.Fprintf(&buf, "IDEMPOTENCY:%s\n", op.IdempotencyKey)
	fmt.Fprintf(&buf, "CAPABILITY:%s\n", op.RequiredCapability)
	buf.WriteString("SCOPES:")
	if err := encodeStringSlice(&buf, sortedScopes); err != nil {
		return "", err
	}
	buf.WriteString("\n")
	fmt.Fprintf(&buf, "EVIDENCE_SENSITIVITY:%s\n", op.EvidenceSensitivity)
	buf.WriteString("PARAMS:")
	buf.Write(canonicalParams)
	buf.WriteString("\n")

	digest := sha256.Sum256(buf.Bytes())
	return Fingerprint("sha256:" + hex.EncodeToString(digest[:])), nil
}

// ComputeIdempotencyFingerprint calculates the canonical idempotency-equivalence fingerprint
// for an operation, which excludes the execution deadline.
func ComputeIdempotencyFingerprint(op Operation) (Fingerprint, error) {
	return computeFingerprintInternal(op, false)
}

// ComputeFingerprint is a helper that constructs an Operation and computes its fingerprint.
func ComputeFingerprint(
	caller ActorID,
	effective ActorID,
	target MachineRef,
	kind OperationKind,
	class OperationClass,
	reason string,
	deadline time.Time,
	idempotencyKey string,
	capability string,
	scopes []string,
	evidenceSensitivity EvidenceSensitivity,
	params map[string]any,
) (Fingerprint, error) {
	op := Operation{
		Kind:                kind,
		Target:              target,
		Actor:               ActorContext{AuthenticatedCaller: caller, EffectiveActor: effective},
		Reason:              reason,
		Deadline:            deadline,
		IdempotencyKey:      idempotencyKey,
		RequiredCapability:  capability,
		RequiredScopes:      scopes,
		Classification:      class,
		EvidenceSensitivity: evidenceSensitivity,
		Parameters:          params,
	}
	return ComputeOperationFingerprint(op)
}
