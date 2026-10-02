package domain

import (
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrInvalidDesktopRequest = errors.New("invalid guest desktop request")

func (r DesktopRequest) ObserveOnly() bool {
	switch r.Action {
	case "status", "cursor", "windows", "uia.tree", "clipboard.get":
		return true
	default:
		return false
	}
}

// Validate bounds the helper protocol before either observation or mutation dispatch.
func (r DesktopRequest) Validate() error {
	if !canonicalHex(r.RequestID, 32) {
		return ErrInvalidDesktopRequest
	}
	deadline, err := time.Parse(time.RFC3339Nano, r.Deadline)
	if err != nil || deadline.IsZero() {
		return ErrInvalidDesktopRequest
	}
	if !r.ObserveOnly() {
		if err := validateDesktopActionParams(DesktopActionParameters(r)); err != nil {
			return ErrInvalidDesktopRequest
		}
	}
	if len(r.Text) > 65536 || !utf8.ValidString(r.Text) || strings.ContainsRune(r.Text, 0) || len(r.Arguments) > 64 || len(r.Executable) > 1024 || strings.ContainsRune(r.Executable, 0) {
		return ErrInvalidDesktopRequest
	}
	for _, arg := range r.Arguments {
		if len(arg) > 4096 || !utf8.ValidString(arg) || strings.ContainsRune(arg, 0) {
			return ErrInvalidDesktopRequest
		}
	}
	if len(r.ElementID) > 256 || strings.ContainsAny(r.ElementID, "\r\n\x00") {
		return ErrInvalidDesktopRequest
	}
	if r.WindowID != "" {
		if _, err := strconv.ParseUint(r.WindowID, 10, 64); err != nil {
			return ErrInvalidDesktopRequest
		}
	}
	if strings.HasPrefix(r.Action, "window.") && r.WindowID == "" {
		return ErrInvalidDesktopRequest
	}
	if r.Action == "launch" && r.Executable == "" {
		return ErrInvalidDesktopRequest
	}
	if strings.HasPrefix(r.Action, "uia.") && r.Action != "uia.tree" && r.ElementID == "" {
		return ErrInvalidDesktopRequest
	}
	if r.Axis != "" && r.Axis != "vertical" && r.Axis != "horizontal" {
		return ErrInvalidDesktopRequest
	}
	if r.X < -65535 || r.X > 65535 || r.Y < -65535 || r.Y > 65535 || r.Width < 0 || r.Width > 65535 || r.Height < 0 || r.Height > 65535 || r.Delta < -12000 || r.Delta > 12000 {
		return ErrInvalidDesktopRequest
	}
	if r.Action == "window.resize" && (r.Width == 0 || r.Height == 0) {
		return ErrInvalidDesktopRequest
	}
	return nil
}
