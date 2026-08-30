package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

func TestSessionIDValidation(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{
			name:    "valid canonical session id",
			id:      "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4",
			wantErr: false,
		},
		{
			name:    "empty id",
			id:      "",
			wantErr: true,
		},
		{
			name:    "wrong prefix",
			id:      "op-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4",
			wantErr: true,
		},
		{
			name:    "too short",
			id:      "sess-1234",
			wantErr: true,
		},
		{
			name:    "too long",
			id:      "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4extra",
			wantErr: true,
		},
		{
			name:    "uppercase hex",
			id:      "sess-A1B2C3D4E5F6A1B2C3D4E5F6A1B2C3D4",
			wantErr: true,
		},
		{
			name:    "invalid characters",
			id:      "sess-g1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sid := domain.SessionID(tt.id)
			err := sid.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("SessionID(%q).Validate() error = %v, wantErr %v", tt.id, err, tt.wantErr)
			}
			if sid.String() != tt.id {
				t.Errorf("SessionID.String() = %q, want %q", sid.String(), tt.id)
			}
		})
	}
}

func TestSessionState(t *testing.T) {
	validStates := []domain.SessionState{
		domain.SessionStateOpening,
		domain.SessionStateActive,
		domain.SessionStateClosing,
		domain.SessionStateClosed,
		domain.SessionStateFailed,
		domain.SessionStateCrashed,
	}

	for _, s := range validStates {
		if !s.IsValid() {
			t.Errorf("state %q expected to be valid", s)
		}
	}

	if domain.SessionState("unknown").IsValid() {
		t.Error("unknown state should not be valid")
	}

	if !domain.SessionStateClosed.IsTerminal() {
		t.Error("closed should be terminal")
	}
	if !domain.SessionStateFailed.IsTerminal() {
		t.Error("failed should be terminal")
	}
	if !domain.SessionStateCrashed.IsTerminal() {
		t.Error("crashed should be terminal")
	}
	if domain.SessionStateActive.IsTerminal() {
		t.Error("active should not be terminal")
	}
	if domain.SessionStateOpening.IsTerminal() {
		t.Error("opening should not be terminal")
	}
}

func TestControlKeyNormalizationAndBytes(t *testing.T) {
	tests := []struct {
		input     string
		wantKey   domain.ControlKey
		wantBytes []byte
		wantErr   bool
	}{
		{"ctrl-c", domain.ControlKeyCtrlC, []byte{0x03}, false},
		{"Ctrl+C", domain.ControlKeyCtrlC, []byte{0x03}, false},
		{"ctrl-d", domain.ControlKeyCtrlD, []byte{0x04}, false},
		{"ctrl-z", domain.ControlKeyCtrlZ, []byte{0x1a}, false},
		{"enter", domain.ControlKeyEnter, []byte("\r\n"), false},
		{"return", domain.ControlKeyEnter, []byte("\r\n"), false},
		{"tab", domain.ControlKeyTab, []byte{0x09}, false},
		{"escape", domain.ControlKeyEscape, []byte{0x1b}, false},
		{"esc", domain.ControlKeyEscape, []byte{0x1b}, false},
		{"up", domain.ControlKeyUp, []byte("\x1b[A"), false},
		{"down", domain.ControlKeyDown, []byte("\x1b[B"), false},
		{"right", domain.ControlKeyRight, []byte("\x1b[C"), false},
		{"left", domain.ControlKeyLeft, []byte("\x1b[D"), false},
		{"backspace", domain.ControlKeyBackspace, []byte{0x7f}, false},
		{"page-up", domain.ControlKeyPageUp, []byte("\x1b[5~"), false},
		{"page-down", domain.ControlKeyPageDown, []byte("\x1b[6~"), false},
		{"invalid-key", "", nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			k, err := domain.NormalizeControlKey(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NormalizeControlKey(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if !tt.wantErr {
				if k != tt.wantKey {
					t.Errorf("got key %v, want %v", k, tt.wantKey)
				}
				if string(k.ToBytes()) != string(tt.wantBytes) {
					t.Errorf("got bytes %v, want %v", k.ToBytes(), tt.wantBytes)
				}
			}
		})
	}
}

func TestTerminalDimensions(t *testing.T) {
	if err := domain.ValidateTerminalDimensions(80, 24); err != nil {
		t.Errorf("standard dimensions failed: %v", err)
	}
	if err := domain.ValidateTerminalDimensions(10, 24); err == nil {
		t.Error("expected error for cols < 20")
	}
	if err := domain.ValidateTerminalDimensions(80, 2); err == nil {
		t.Error("expected error for rows < 5")
	}
	if err := domain.ValidateTerminalDimensions(600, 24); err == nil {
		t.Error("expected error for cols > 500")
	}
	if err := domain.ValidateTerminalDimensions(80, 300); err == nil {
		t.Error("expected error for rows > 200")
	}
}

func TestTerminalTypeValidation(t *testing.T) {
	for _, term := range []string{"xterm", "xterm-256color", "screen.xterm_256+color"} {
		if err := domain.ValidateTerminalType(term); err != nil {
			t.Fatalf("valid terminal %q: %v", term, err)
		}
	}
	for _, term := range []string{"", " xterm", "xterm 256", "xterm;rm", "xterm\x1b[31m", string([]byte{0xff})} {
		if err := domain.ValidateTerminalType(term); err == nil {
			t.Fatalf("invalid terminal %q accepted", term)
		}
	}
}

func TestSessionObservation(t *testing.T) {
	now := time.Now().UTC()
	obs := domain.SessionObservation{
		ID:              "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4",
		Target:          "c4a523d4-6b99-4d62-a5e2-4752c0f20001",
		OwnerActor:      "agent:mcp-local",
		State:           domain.SessionStateActive,
		CreatedAt:       now,
		LastActivityAt:  now,
		Cols:            80,
		Rows:            24,
		TermType:        "xterm-256color",
		ObservationType: domain.ObservationObserved,
	}

	if err := obs.Validate(); err != nil {
		t.Errorf("observation invalid: %v", err)
	}
}

func TestSessionOperationValidation(t *testing.T) {
	sessID := "sess-a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4"

	// session.open
	if err := domain.ValidateOperationParameters("session.open", map[string]any{"cols": 80, "rows": 24, "term": "xterm-256color"}); err != nil {
		t.Errorf("valid session.open params failed: %v", err)
	}
	if err := domain.ValidateOperationParameters("session.open", map[string]any{"invalid": 123}); err == nil {
		t.Error("expected error for invalid session.open param")
	}

	// session.read
	if err := domain.ValidateOperationParameters("session.read", map[string]any{"session_id": sessID, "after_seq": 0, "limit_bytes": 1024}); err != nil {
		t.Errorf("valid session.read params failed: %v", err)
	}
	if err := domain.ValidateOperationParameters("session.read", map[string]any{"after_seq": 0}); err == nil {
		t.Error("expected error for session.read missing session_id")
	}

	// session.write
	if err := domain.ValidateOperationParameters("session.write", map[string]any{"session_id": sessID, "data": "hello\n"}); err != nil {
		t.Errorf("valid session.write params failed: %v", err)
	}
	if err := domain.ValidateOperationParameters("session.write", map[string]any{"session_id": sessID, "data": strings.Repeat("x", 70000)}); err == nil {
		t.Error("expected error for session.write data exceeding limit")
	}

	// session.control
	if err := domain.ValidateOperationParameters("session.control", map[string]any{"session_id": sessID, "key": "ctrl-c"}); err != nil {
		t.Errorf("valid session.control params failed: %v", err)
	}
	if err := domain.ValidateOperationParameters("session.control", map[string]any{"session_id": sessID, "key": "bad-key"}); err == nil {
		t.Error("expected error for session.control invalid key")
	}

	// session.wait
	if err := domain.ValidateOperationParameters("session.wait", map[string]any{"session_id": sessID, "settle_ms": 500, "regex": "ready"}); err != nil {
		t.Errorf("valid session.wait params failed: %v", err)
	}

	// session.close
	if err := domain.ValidateOperationParameters("session.close", map[string]any{"session_id": sessID}); err != nil {
		t.Errorf("valid session.close params failed: %v", err)
	}

	// session.list
	if err := domain.ValidateOperationParameters("session.list", map[string]any{"machine": "c4a523d4-6b99-4d62-a5e2-4752c0f20001"}); err != nil {
		t.Errorf("valid session.list params failed: %v", err)
	}

	// session.show
	if err := domain.ValidateOperationParameters("session.show", map[string]any{"session_id": sessID}); err != nil {
		t.Errorf("valid session.show params failed: %v", err)
	}
}
