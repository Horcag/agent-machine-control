package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
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
	// MaxApprovalIDLength bounds canonical approval identifiers used as store filenames.
	MaxApprovalIDLength = 128
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

// DeriveApprovalIdempotencyKey creates a deterministic derived key for an approved retry attempt.
func DeriveApprovalIdempotencyKey(originalKey string) string {
	if originalKey == "" {
		return ""
	}
	// Derive a safe collision-resistant bounded key
	hash := sha256.Sum256([]byte(originalKey + "\x00approved"))
	return hex.EncodeToString(hash[:])
}

// ValidateReason checks that an operation reason is valid and bounded.
func ValidateReason(s string) error {
	if s == "" {
		return ErrMissingReason
	}
	return ValidateBoundedString(s, MinReasonLength, MaxReasonLength, ErrInvalidReason)
}

// ValidateApprovalID checks that an approval identifier is canonical and safe for filename use.
func ValidateApprovalID(s string) error {
	if len(s) < 1 || len(s) > MaxApprovalIDLength {
		return fmt.Errorf("%w: approval ID length %d out of bounds [1, %d]", ErrInvalidApprovalRecord, len(s), MaxApprovalIDLength)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		alphanumeric := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if alphanumeric {
			continue
		}
		if (c == '-' || c == '_') && i > 0 && i < len(s)-1 {
			continue
		}
		return fmt.Errorf("%w: approval ID must use ASCII alphanumeric characters with internal '-' or '_' separators", ErrInvalidApprovalRecord)
	}
	return nil
}

// ValidateOperationID checks that an operation identifier matches the canonical op-<32 lowercase hex> format.
func ValidateOperationID(s string) error {
	if len(s) != 35 || !strings.HasPrefix(s, "op-") {
		return fmt.Errorf("%w: operation ID must be 'op-' followed by 32 lowercase hex characters", ErrInvalidOperationID)
	}
	hexPart := s[3:]
	for i := 0; i < len(hexPart); i++ {
		c := hexPart[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("%w: operation ID contains non-hexadecimal character %q", ErrInvalidOperationID, c)
		}
	}
	return nil
}

// ValidateReceiptID checks that a receipt identifier matches the canonical rcpt-<32 lowercase hex> format.
func ValidateReceiptID(s string) error {
	if len(s) != 37 || !strings.HasPrefix(s, "rcpt-") {
		return fmt.Errorf("%w: receipt ID must be 'rcpt-' followed by 32 lowercase hex characters", ErrInvalidReceiptID)
	}
	hexPart := s[5:]
	for i := 0; i < len(hexPart); i++ {
		c := hexPart[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("%w: receipt ID contains non-hexadecimal character %q", ErrInvalidReceiptID, c)
		}
	}
	return nil
}

// ValidatePathSafeID checks that an identifier contains no path separators, traversal elements, or control characters.
func ValidatePathSafeID(s string, errBase error) error {
	if err := ValidateBoundedString(s, 1, 256, errBase); err != nil {
		return err
	}
	if strings.Contains(s, "/") || strings.Contains(s, "\\") || strings.Contains(s, "..") {
		return fmt.Errorf("%w: identifier cannot contain path separators or traversal characters", errBase)
	}
	return nil
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

// ValidateOperationParameters validates that operation parameters strictly match canonical schemas for known kinds.
func ValidateOperationParameters(kind OperationKind, params map[string]any) error {
	switch kind {
	case "machine.start":
		return validateStartParams(params)
	case "machine.stop":
		return validateStopParams(params)
	case "checkpoint.create":
		return validateCheckpointCreateParams(params)
	case "checkpoint.restore":
		return validateCheckpointRestoreParams(params)
	case "session.open":
		return validateSessionOpenParams(params)
	case "session.read":
		return validateSessionReadParams(params)
	case "session.write":
		return validateSessionWriteParams(params)
	case "session.control":
		return validateSessionControlParams(params)
	case "session.wait":
		return validateSessionWaitParams(params)
	case "session.list":
		return validateSessionListParams(params)
	case "session.show":
		return validateSessionShowParams(params)
	case "session.close":
		return validateSessionCloseParams(params)
	case "session.approval.issue":
		return validateSessionApprovalIssueParams(params)
	default:
		return ErrInvalidOperationKind
	}
}

func validateSessionApprovalIssueParams(params map[string]any) error {
	required := []string{"approval_id", "approved_fingerprint", "approved_kind", "beneficiary", "deadline"}
	if len(params) != len(required) {
		return fmt.Errorf("%w: session.approval.issue requires exactly the canonical issuance fields", ErrNonCanonicalParameter)
	}
	for _, key := range required {
		value, ok := params[key].(string)
		if !ok || value == "" {
			return fmt.Errorf("%w: session.approval.issue field %s must be a non-empty string", ErrNonCanonicalParameter, key)
		}
	}
	if err := ValidateApprovalID(params["approval_id"].(string)); err != nil {
		return fmt.Errorf("%w: invalid approval_id", ErrNonCanonicalParameter)
	}
	if err := Fingerprint(params["approved_fingerprint"].(string)).Validate(); err != nil {
		return fmt.Errorf("%w: invalid approved_fingerprint", ErrNonCanonicalParameter)
	}
	if err := OperationKind(params["approved_kind"].(string)).Validate(); err != nil {
		return fmt.Errorf("%w: invalid approved_kind", ErrNonCanonicalParameter)
	}
	if err := ActorID(params["beneficiary"].(string)).Validate(); err != nil {
		return fmt.Errorf("%w: invalid beneficiary", ErrNonCanonicalParameter)
	}
	deadline, err := time.Parse(time.RFC3339Nano, params["deadline"].(string))
	if err != nil || deadline.UTC().Format(time.RFC3339Nano) != params["deadline"].(string) {
		return fmt.Errorf("%w: deadline must be canonical RFC3339Nano UTC", ErrNonCanonicalParameter)
	}
	return nil
}

// ValidateSessionID checks that a session identifier matches the canonical sess-<32 lowercase hex> format.
func ValidateSessionID(s string) error {
	if len(s) != 37 || !strings.HasPrefix(s, "sess-") {
		return fmt.Errorf("%w: session ID must be 'sess-' followed by 32 lowercase hex characters", ErrInvalidSessionID)
	}
	hexPart := s[5:]
	for i := 0; i < len(hexPart); i++ {
		c := hexPart[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("%w: session ID contains non-hexadecimal character %q", ErrInvalidSessionID, c)
		}
	}
	return nil
}

// ValidateControlKey checks that a control key is a recognized and valid key identifier.
func ValidateControlKey(s string) error {
	_, err := NormalizeControlKey(s)
	return err
}

// ValidateTerminalDimensions validates columns and rows within allowed bounds.
func ValidateTerminalDimensions(cols, rows uint16) error {
	if cols < MinCols || cols > MaxCols {
		return fmt.Errorf("%w: cols %d out of bounds [%d, %d]", ErrInvalidTerminalDimensions, cols, MinCols, MaxCols)
	}
	if rows < MinRows || rows > MaxRows {
		return fmt.Errorf("%w: rows %d out of bounds [%d, %d]", ErrInvalidTerminalDimensions, rows, MinRows, MaxRows)
	}
	return nil
}

// ValidateTerminalType accepts only canonical ASCII SSH PTY identifiers.
func ValidateTerminalType(term string) error {
	if len(term) == 0 || len(term) > 64 || !utf8.ValidString(term) {
		return ErrInvalidTerminalType
	}
	for i := 0; i < len(term); i++ {
		c := term[i]
		if validTerminalTypeByte(c, i > 0) {
			continue
		}
		return fmt.Errorf("%w: unsupported byte %q", ErrInvalidTerminalType, c)
	}
	return nil
}

func validTerminalTypeByte(c byte, allowPunctuation bool) bool {
	alphanumeric := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
	punctuation := c == '-' || c == '_' || c == '.' || c == '+'
	return alphanumeric || (allowPunctuation && punctuation)
}

func validateStartParams(params map[string]any) error {
	if len(params) > 0 {
		return fmt.Errorf("%w: machine.start does not accept parameters", ErrNonCanonicalParameter)
	}
	return nil
}

func validateStopParams(params map[string]any) error {
	if len(params) == 0 {
		return nil
	}
	if len(params) > 1 {
		return fmt.Errorf("%w: machine.stop only accepts 'mode' parameter", ErrNonCanonicalParameter)
	}
	modeVal, ok := params["mode"]
	if !ok {
		return fmt.Errorf("%w: unexpected parameter for machine.stop", ErrNonCanonicalParameter)
	}
	mode, ok := modeVal.(string)
	if !ok {
		return fmt.Errorf("%w: mode must be a string", ErrNonCanonicalParameter)
	}
	if mode != "shutdown" && mode != "save" && mode != "turn-off" {
		return fmt.Errorf("%w: invalid mode %q for machine.stop (must be shutdown, save, or turn-off)", ErrNonCanonicalParameter, mode)
	}
	return nil
}

func validateCheckpointCreateParams(params map[string]any) error {
	if len(params) == 0 {
		return nil
	}
	if len(params) > 1 {
		return fmt.Errorf("%w: checkpoint.create only accepts 'name' parameter", ErrNonCanonicalParameter)
	}
	nameVal, ok := params["name"]
	if !ok {
		return fmt.Errorf("%w: unexpected parameter for checkpoint.create", ErrNonCanonicalParameter)
	}
	name, ok := nameVal.(string)
	if !ok {
		return fmt.Errorf("%w: name must be a string", ErrNonCanonicalParameter)
	}
	if err := ValidateBoundedString(name, 1, 256, ErrInvalidCheckpointObservation); err != nil {
		return fmt.Errorf("%w: invalid checkpoint name: %w", ErrNonCanonicalParameter, err)
	}
	return nil
}

func validateCheckpointRestoreParams(params map[string]any) error {
	if len(params) != 1 {
		return fmt.Errorf("%w: checkpoint.restore requires exactly 'checkpoint_id' parameter", ErrNonCanonicalParameter)
	}
	chkVal, ok := params["checkpoint_id"]
	if !ok {
		return fmt.Errorf("%w: checkpoint.restore requires 'checkpoint_id' parameter", ErrNonCanonicalParameter)
	}
	chkID, ok := chkVal.(string)
	if !ok {
		return fmt.Errorf("%w: checkpoint_id must be a string", ErrNonCanonicalParameter)
	}
	if err := ValidateMachineGUID(chkID); err != nil {
		return fmt.Errorf("%w: invalid checkpoint GUID: %w", ErrNonCanonicalParameter, err)
	}
	return nil
}
