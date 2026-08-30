package domain

// Canonical failure categories are the only public error classes that may be
// persisted for a failed operation. Their messages are fixed so receipts never
// retain backend-controlled error text.
const (
	FailureCategoryCallerCanceled            = "caller_canceled"
	FailureCategoryDeadlineExceeded          = "deadline_exceeded"
	FailureCategorySessionNotFound           = "session_not_found"
	FailureCategorySessionAccessDenied       = "session_access_denied"
	FailureCategorySessionClosed             = "session_closed"
	FailureCategorySessionConflict           = "session_conflict"
	FailureCategorySessionWaitTimeout        = "session_wait_timeout"
	FailureCategoryHostKeyMismatch           = "host_key_mismatch"
	FailureCategoryMissingHostKeyPin         = "missing_host_key_pin"
	FailureCategoryNonCanonicalParameter     = "non_canonical_parameter"
	FailureCategoryInvalidControlKey         = "invalid_control_key"
	FailureCategoryInvalidTerminalDimensions = "invalid_terminal_dimensions"
	FailureCategoryInvalidTerminalType       = "invalid_terminal_type"
	FailureCategoryInvalidApprovalRecord     = "invalid_approval_record"
	FailureCategoryMissingDeadline           = "missing_deadline"
)

var canonicalFailureMessages = map[string]string{
	FailureCategoryCallerCanceled:            "operation canceled by caller",
	FailureCategoryDeadlineExceeded:          "operation deadline exceeded",
	FailureCategorySessionNotFound:           "session not found",
	FailureCategorySessionAccessDenied:       "session access denied",
	FailureCategorySessionClosed:             "session is closed",
	FailureCategorySessionConflict:           "session conflict",
	FailureCategorySessionWaitTimeout:        "session wait timeout",
	FailureCategoryHostKeyMismatch:           "guest host key verification failed",
	FailureCategoryMissingHostKeyPin:         "guest host key pin missing",
	FailureCategoryNonCanonicalParameter:     "non-canonical session parameter",
	FailureCategoryInvalidControlKey:         "invalid session control key",
	FailureCategoryInvalidTerminalDimensions: "invalid terminal dimensions",
	FailureCategoryInvalidTerminalType:       "invalid terminal type",
	FailureCategoryInvalidApprovalRecord:     "invalid approval record",
	FailureCategoryMissingDeadline:           "operation deadline missing",
}

// CanonicalFailureMessage returns the fixed receipt message for an allowlisted
// public failure category.
func CanonicalFailureMessage(category string) (string, bool) {
	message, ok := canonicalFailureMessages[category]
	return message, ok
}
