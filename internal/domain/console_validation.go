package domain

import (
	"errors"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

var ErrInvalidConsoleInput = errors.New("invalid console input")
var ErrInvalidConsoleDimensions = errors.New("invalid console dimensions")

// ValidateConsoleDimensions bounds allocation and the WMI uint16 dimensions.
func ValidateConsoleDimensions(width, height int) error {
	if width < 1 || height < 1 || width > 65535 || height > 65535 || width > MaxConsolePixels/height {
		return ErrInvalidConsoleDimensions
	}
	return nil
}

// Validate rejects unsupported actions and bounds guest input without exposing text.
func (in ConsoleInput) Validate() error {
	if in.Delta != 0 {
		return ErrInvalidConsoleInput
	}
	switch in.Kind {
	case "move", "click", "drag":
		return in.validatePointer()
	case "key", "type":
		return in.validateKeyboard()
	default:
		return ErrInvalidConsoleInput
	}
}

func consolePositionValid(x, y int) bool { return x >= 0 && y >= 0 && x <= 65535 && y <= 65535 }

func (in ConsoleInput) validatePointer() error {
	if in.FrameID == "" || len(in.FrameID) > 128 || !consolePositionValid(in.X, in.Y) || in.Key != "" || in.Text != "" {
		return ErrInvalidConsoleInput
	}
	if in.Kind == "move" {
		if in.Button != "" {
			return ErrInvalidConsoleInput
		}
	} else if in.Button != "left" && in.Button != "right" && in.Button != "middle" {
		return ErrInvalidConsoleInput
	}
	if in.Kind == "drag" {
		if !consolePositionValid(in.ToX, in.ToY) {
			return ErrInvalidConsoleInput
		}
	} else if in.ToX != 0 || in.ToY != 0 {
		return ErrInvalidConsoleInput
	}
	return nil
}

func (in ConsoleInput) validateKeyboard() error {
	if in.FrameID != "" || in.X != 0 || in.Y != 0 || in.ToX != 0 || in.ToY != 0 || in.Button != "" {
		return ErrInvalidConsoleInput
	}
	if in.Kind == "key" {
		if in.Text != "" {
			return ErrInvalidConsoleInput
		}
		_, err := ConsoleKeyCodes(in.Key)
		return err
	}
	return in.validateText()
}

func (in ConsoleInput) validateText() error {
	if in.Key != "" || in.Text == "" || !utf8.ValidString(in.Text) || len(in.Text) > 1024 || strings.ContainsRune(in.Text, 0) || len(utf16.Encode([]rune(in.Text))) > 256 {
		return ErrInvalidConsoleInput
	}
	return nil
}

// ConsoleKeyCodes translates a bounded chord into Windows virtual-key codes.
func ConsoleKeyCodes(key string) ([]uint32, error) {
	if len(key) > 64 {
		return nil, ErrInvalidConsoleInput
	}
	parts := strings.Split(strings.ToLower(key), "+")
	if len(parts) > 4 {
		return nil, ErrInvalidConsoleInput
	}
	named := map[string]uint32{"ctrl": 17, "alt": 18, "shift": 16, "win": 91, "enter": 13, "tab": 9, "escape": 27, "esc": 27, "space": 32, "backspace": 8, "delete": 46, "home": 36, "end": 35, "pageup": 33, "pagedown": 34, "left": 37, "up": 38, "right": 39, "down": 40}
	codes := make([]uint32, 0, len(parts))
	seen := map[uint32]bool{}
	for index, part := range parts {
		code, ok := named[part]
		if len(part) == 1 && ((part[0] >= 'a' && part[0] <= 'z') || (part[0] >= '0' && part[0] <= '9')) {
			code = uint32(strings.ToUpper(part)[0])
			ok = true
		}
		if !ok || seen[code] || (index < len(parts)-1 && !consoleModifier(code)) {
			return nil, ErrInvalidConsoleInput
		}
		seen[code] = true
		codes = append(codes, code)
	}
	return codes, nil
}

func consoleModifier(code uint32) bool { return code == 16 || code == 17 || code == 18 || code == 91 }
