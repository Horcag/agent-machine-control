package domain

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ValidateBoundedString checks that a string is within length bounds, contains valid UTF-8,
// has no leading or trailing whitespace, and contains no control characters.
func ValidateBoundedString(s string, minLen, maxLen int, errBase error) error {
	if len(s) < minLen || len(s) > maxLen {
		return fmt.Errorf("%w: length %d out of bounds [%d, %d]", errBase, len(s), minLen, maxLen)
	}
	if !utf8.ValidString(s) {
		return fmt.Errorf("%w: contains invalid UTF-8 bytes", errBase)
	}
	if strings.TrimSpace(s) != s {
		return fmt.Errorf("%w: contains leading or trailing whitespace", errBase)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: contains control character U+%04X", errBase, r)
		}
	}
	return nil
}

const (
	// MinReasonLength is the minimum length of an operation reason.
	MinReasonLength = 1
	// MaxReasonLength is the maximum length of an operation reason.
	MaxReasonLength = 1024

	// MinIdempotencyKeyLength is the minimum length of an idempotency key.
	MinIdempotencyKeyLength = 1
	// MaxIdempotencyKeyLength is the maximum length of an idempotency key.
	MaxIdempotencyKeyLength = 256
)

// ValidateScope checks that a scope string is a valid canonical identifier.
func ValidateScope(s string) error {
	return ValidateBoundedString(s, 1, 256, ErrInvalidScope)
}

// ValidateCapability checks that a capability string is a valid canonical identifier.
func ValidateCapability(s string) error {
	return ValidateBoundedString(s, 1, 256, ErrInvalidCapability)
}

// ValidateIdempotencyKey checks that an idempotency key is non-empty and well-formed.
func ValidateIdempotencyKey(s string) error {
	if s == "" {
		return ErrMissingIdempotencyKey
	}
	return ValidateBoundedString(s, MinIdempotencyKeyLength, MaxIdempotencyKeyLength, ErrInvalidIdempotencyKey)
}

// ValidateReason checks that an operation reason is valid and bounded.
func ValidateReason(s string) error {
	if s == "" {
		return ErrMissingReason
	}
	return ValidateBoundedString(s, MinReasonLength, MaxReasonLength, ErrInvalidReason)
}

// ValidateApprovalID checks that an approval identifier is non-empty and well-formed.
func ValidateApprovalID(s string) error {
	return ValidateBoundedString(s, 1, 256, ErrInvalidApprovalRecord)
}

// ValidateReceiptID checks that a receipt identifier is non-empty and well-formed.
func ValidateReceiptID(s string) error {
	return ValidateBoundedString(s, 1, 256, ErrInvalidReceiptID)
}

// ValidateBackendID checks that an effective backend identifier is non-empty and well-formed.
func ValidateBackendID(s string) error {
	return ValidateBoundedString(s, 1, 256, ErrInvalidBackendID)
}

// ValidateRollbackRef checks that a rollback checkpoint reference is non-empty and well-formed.
func ValidateRollbackRef(s string) error {
	return ValidateBoundedString(s, 1, 256, ErrInvalidRollbackRef)
}

// ValidateEvidenceRef checks that an evidence reference is a bounded opaque hash/ID, not a file path.
func ValidateEvidenceRef(s string) error {
	if err := ValidateBoundedString(s, 1, 256, ErrInvalidEvidenceRef); err != nil {
		return err
	}
	if strings.Contains(s, "/") || strings.Contains(s, "\\") {
		return fmt.Errorf("%w: evidence ref must be opaque id or content hash, not file path", ErrInvalidEvidenceRef)
	}
	for _, r := range s {
		if unicode.IsSpace(r) {
			return fmt.Errorf("%w: evidence ref contains space character", ErrInvalidEvidenceRef)
		}
	}
	return nil
}
