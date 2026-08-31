package domain

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// SessionID uniquely references an active or closed terminal session.
// Canonical format: "sess-" followed by 32 lowercase hex characters (37 chars total).
type SessionID string

// String returns the string representation of the SessionID.
func (s SessionID) String() string {
	return string(s)
}

// Validate checks that the SessionID matches the canonical format sess-<32 hex chars>.
func (s SessionID) Validate() error {
	return ValidateSessionID(string(s))
}

// GenerateSessionID creates a new canonical SessionID (sess-<32 hex chars>).
func GenerateSessionID() (SessionID, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate session id: %w", err)
	}
	return SessionID(fmt.Sprintf("sess-%s", hex.EncodeToString(b))), nil
}

// SessionState defines the lifecycle state of a persistent terminal session.
type SessionState string

const (
	SessionStateOpening SessionState = "opening"
	SessionStateActive  SessionState = "active"
	SessionStateClosing SessionState = "closing"
	SessionStateClosed  SessionState = "closed"
	SessionStateFailed  SessionState = "failed"
	SessionStateCrashed SessionState = "crashed"
)

// IsValid returns true if the SessionState is a recognized state enum value.
func (s SessionState) IsValid() bool {
	switch s {
	case SessionStateOpening, SessionStateActive, SessionStateClosing, SessionStateClosed, SessionStateFailed, SessionStateCrashed:
		return true
	default:
		return false
	}
}

// IsTerminal returns true if the session has reached a final non-active state.
func (s SessionState) IsTerminal() bool {
	return s == SessionStateClosed || s == SessionStateFailed || s == SessionStateCrashed
}

// ControlKey defines standard terminal control keystrokes and escape actions.
type ControlKey string

const (
	ControlKeyCtrlC     ControlKey = "ctrl-c"    // \x03 (ETX)
	ControlKeyCtrlD     ControlKey = "ctrl-d"    // \x04 (EOT)
	ControlKeyCtrlZ     ControlKey = "ctrl-z"    // \x1a (SUB)
	ControlKeyEnter     ControlKey = "enter"     // \r\n
	ControlKeyTab       ControlKey = "tab"       // \t
	ControlKeyEscape    ControlKey = "escape"    // \x1b
	ControlKeyUp        ControlKey = "up"        // \x1b[A
	ControlKeyDown      ControlKey = "down"      // \x1b[B
	ControlKeyRight     ControlKey = "right"     // \x1b[C
	ControlKeyLeft      ControlKey = "left"      // \x1b[D
	ControlKeyBackspace ControlKey = "backspace" // \x7f
	ControlKeyPageUp    ControlKey = "page-up"   // \x1b[5~
	ControlKeyPageDown  ControlKey = "page-down" // \x1b[6~
)

// NormalizeControlKey parses and normalizes human/MCP representations into canonical ControlKey.
func NormalizeControlKey(raw string) (ControlKey, error) {
	norm := strings.TrimSpace(strings.ToLower(raw))
	norm = strings.ReplaceAll(norm, "+", "-")
	norm = strings.ReplaceAll(norm, "_", "-")
	norm = strings.ReplaceAll(norm, "arrow", "")

	switch norm {
	case "ctrl-c", "ctrlc", "c":
		return ControlKeyCtrlC, nil
	case "ctrl-d", "ctrld", "d":
		return ControlKeyCtrlD, nil
	case "ctrl-z", "ctrlz", "z":
		return ControlKeyCtrlZ, nil
	case "enter", "return", "cr", "lf":
		return ControlKeyEnter, nil
	case "tab":
		return ControlKeyTab, nil
	case "escape", "esc":
		return ControlKeyEscape, nil
	case "up", "arrowup":
		return ControlKeyUp, nil
	case "down", "arrowdown":
		return ControlKeyDown, nil
	case "right", "arrowright":
		return ControlKeyRight, nil
	case "left", "arrowleft":
		return ControlKeyLeft, nil
	case "backspace", "bs":
		return ControlKeyBackspace, nil
	case "pageup", "page-up", "pgup":
		return ControlKeyPageUp, nil
	case "pagedown", "page-down", "pgdn":
		return ControlKeyPageDown, nil
	default:
		return "", fmt.Errorf("%w: unknown control key %q", ErrInvalidControlKey, raw)
	}
}

// ToBytes returns the raw ANSI/VT byte sequence corresponding to the control key.
func (k ControlKey) ToBytes() []byte {
	switch k {
	case ControlKeyCtrlC:
		return []byte{0x03}
	case ControlKeyCtrlD:
		return []byte{0x04}
	case ControlKeyCtrlZ:
		return []byte{0x1a}
	case ControlKeyEnter:
		return []byte("\r\n")
	case ControlKeyTab:
		return []byte{0x09}
	case ControlKeyEscape:
		return []byte{0x1b}
	case ControlKeyUp:
		return []byte("\x1b[A")
	case ControlKeyDown:
		return []byte("\x1b[B")
	case ControlKeyRight:
		return []byte("\x1b[C")
	case ControlKeyLeft:
		return []byte("\x1b[D")
	case ControlKeyBackspace:
		return []byte{0x7f}
	case ControlKeyPageUp:
		return []byte("\x1b[5~")
	case ControlKeyPageDown:
		return []byte("\x1b[6~")
	default:
		return nil
	}
}

const (
	// DefaultCols is the default terminal column count.
	DefaultCols uint16 = 80
	// MinCols is the minimum allowed terminal column count.
	MinCols uint16 = 20
	// MaxCols is the maximum allowed terminal column count.
	MaxCols uint16 = 500

	// DefaultRows is the default terminal row count.
	DefaultRows uint16 = 24
	// MinRows is the minimum allowed terminal row count.
	MinRows uint16 = 5
	// MaxRows is the maximum allowed terminal row count.
	MaxRows uint16 = 200

	// DefaultTermType is the default terminal emulation identifier.
	DefaultTermType = "xterm-256color"

	// MaxSessionWriteBytes is the maximum bytes permitted per single write call (64 KB).
	MaxSessionWriteBytes = 64 * 1024

	// MaxSessionRegexPatternLength is the maximum length of a wait regex pattern.
	MaxSessionRegexPatternLength = 512

	// DefaultSettleTime is the default settle quiet period before wait resolves.
	DefaultSettleTime = 500 * time.Millisecond

	// DefaultWaitTimeout is the default maximum wait duration.
	DefaultWaitTimeout = 30 * time.Second

	// MaxWaitDuration is the maximum allowed wait timeout.
	MaxWaitDuration = 5 * time.Minute
)

// SessionChunk represents a sequenced slice of sanitized terminal text output.
type SessionChunk struct {
	Seq       uint64    `json:"seq"`
	Timestamp time.Time `json:"timestamp"`
	Data      string    `json:"data"`
	LossBytes uint64    `json:"loss_bytes,omitempty"`
}

// SessionObservation captures the operational state and metrics of a terminal session.
type SessionObservation struct {
	ID              SessionID       `json:"session_id"`
	Target          MachineRef      `json:"target"`
	OwnerActor      ActorID         `json:"owner_actor"`
	State           SessionState    `json:"state"`
	CreatedAt       time.Time       `json:"created_at"`
	ClosedAt        *time.Time      `json:"closed_at,omitempty"`
	LastActivityAt  time.Time       `json:"last_activity_at"`
	BytesRead       uint64          `json:"bytes_read"`
	BytesWritten    uint64          `json:"bytes_written"`
	Cols            uint16          `json:"cols"`
	Rows            uint16          `json:"rows"`
	TermType        string          `json:"term_type"`
	ExitCode        *int            `json:"exit_code,omitempty"`
	ErrorMessage    string          `json:"error_message,omitempty"`
	ObservationType ObservationType `json:"observation_type"`
}

// Validate checks the complete durable session observation contract.
func (o SessionObservation) Validate() error {
	if err := o.ID.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSessionObservation, err)
	}
	if err := ValidateSessionTarget(o.Target); err != nil {
		return fmt.Errorf("%w: invalid target", ErrInvalidSessionObservation)
	}
	if err := o.OwnerActor.Validate(); err != nil {
		return fmt.Errorf("%w: invalid owner", ErrInvalidSessionObservation)
	}
	if !o.State.IsValid() || o.CreatedAt.IsZero() || o.LastActivityAt.IsZero() || o.LastActivityAt.Before(o.CreatedAt) {
		return fmt.Errorf("%w: invalid lifecycle state or timestamps", ErrInvalidSessionObservation)
	}
	if o.State.IsTerminal() != (o.ClosedAt != nil) {
		return fmt.Errorf("%w: closed timestamp does not match state", ErrInvalidSessionObservation)
	}
	if o.ClosedAt != nil && o.ClosedAt.Before(o.CreatedAt) {
		return fmt.Errorf("%w: closed timestamp precedes creation", ErrInvalidSessionObservation)
	}
	if err := ValidateTerminalDimensions(o.Cols, o.Rows); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSessionObservation, err)
	}
	if err := ValidateTerminalType(o.TermType); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSessionObservation, err)
	}
	if o.ObservationType != ObservationObserved {
		return fmt.Errorf("%w: invalid observation type", ErrInvalidSessionObservation)
	}
	return nil
}

// ValidateSessionTarget accepts canonical locators for newly opened sessions
// and GUID-only targets retained in durable Task8 session records.
func ValidateSessionTarget(target MachineRef) error {
	if _, err := ParseMachineLocator(string(target)); err == nil {
		return nil
	}
	return ValidateMachineGUID(string(target))
}
