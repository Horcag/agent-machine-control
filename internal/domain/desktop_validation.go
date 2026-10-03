package domain

import (
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrClipboardUncertain = errors.New("desktop: clipboard possibly cleared; reconcile before any retry or restoration")

var ErrInvalidDesktopRequest = errors.New("invalid guest desktop request")

func (r DesktopRequest) ObserveOnly() bool {
	switch r.Action {
	case "status", "cursor", "windows", "uia.tree", "clipboard.get", "clipboard.snapshot":
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
	if !r.validClipboardGuard() || (r.Action == "clipboard.snapshot" && !r.validInventoryObservation()) || !r.validPayload() || !r.validIdentity() || !r.validGeometry() {
		return ErrInvalidDesktopRequest
	}
	return nil
}

func (r DesktopRequest) validPayload() bool {
	if !desktopTextValid(r.Text, 65536) || len(r.Arguments) > 64 || !desktopTextValid(r.Executable, 1024) {
		return false
	}
	for _, arg := range r.Arguments {
		if !desktopTextValid(arg, 4096) {
			return false
		}
	}
	return r.Action != "launch" || r.Executable != ""
}

func desktopTextValid(text string, limit int) bool {
	return len(text) <= limit && utf8.ValidString(text) && !strings.ContainsRune(text, 0)
}

func (r DesktopRequest) validIdentity() bool {
	if len(r.WindowIdentity) > 128 || strings.ContainsAny(r.WindowIdentity, "\r\n\x00") {
		return false
	}
	if len(r.ElementID) > 256 || strings.ContainsAny(r.ElementID, "\r\n\x00") {
		return false
	}
	if r.WindowID != "" {
		if _, err := strconv.ParseUint(r.WindowID, 10, 64); err != nil {
			return false
		}
	}
	if strings.HasPrefix(r.Action, "window.") && r.WindowID == "" {
		return false
	}
	if strings.HasPrefix(r.Action, "uia.") && r.Action != "uia.tree" && r.ElementID == "" {
		return false
	}
	return true
}

func (r DesktopRequest) validGeometry() bool {
	if r.Axis != "" && r.Axis != "vertical" && r.Axis != "horizontal" {
		return false
	}
	if !desktopCoordinateValid(r.X) || !desktopCoordinateValid(r.Y) || !desktopDimensionValid(r.Width) || !desktopDimensionValid(r.Height) || r.Delta < -12000 || r.Delta > 12000 {
		return false
	}
	if r.Action == "window.resize" && (r.Width == 0 || r.Height == 0) {
		return false
	}
	return true
}

func desktopCoordinateValid(value int) bool { return value >= -65535 && value <= 65535 }
func desktopDimensionValid(value int) bool  { return value >= 0 && value <= 65535 }

func (r DesktopRequest) validClipboardGuard() bool {
	if r.ExpectedSequence == nil && r.ExpectedInventory == nil {
		return true
	}
	if r.Action != "clipboard.set" || r.ExpectedSequence == nil || r.ExpectedInventory == nil || *r.ExpectedInventory == nil || len(*r.ExpectedInventory) > 256 {
		return false
	}
	var previous uint32
	for _, format := range *r.ExpectedInventory {
		if format == 0 || format <= previous {
			return false
		}
		previous = format
	}
	return true
}

// An inventory observation cannot carry payload, window, launch or input fields.
func (r DesktopRequest) validInventoryObservation() bool {
	return r.Text == "" && r.Executable == "" && len(r.Arguments) == 0 &&
		r.WindowID == "" && r.WindowIdentity == "" && r.ElementID == "" &&
		r.X == 0 && r.Y == 0 && r.Width == 0 && r.Height == 0 && r.Delta == 0 && r.Axis == ""
}
