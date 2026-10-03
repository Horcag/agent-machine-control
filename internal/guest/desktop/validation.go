package desktop

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

type actionRule struct {
	window, element, text, launch bool
	geometry                      string
}

func requestRule(action string) (actionRule, bool) {
	window := actionRule{window: true}
	rules := map[string]actionRule{
		"status": {}, "cursor": {}, "windows": {}, "clipboard.get": {},
		"clipboard.set": {text: true}, "launch": {launch: true},
		"window.focus": window, "window.close": window, "window.minimize": window,
		"window.maximize": window, "window.restore": window, "uia.tree": window,
		"uia.invoke":    {window: true, element: true},
		"uia.select":    {window: true, element: true},
		"uia.toggle":    {window: true, element: true},
		"uia.expand":    {window: true, element: true},
		"uia.collapse":  {window: true, element: true},
		"uia.scroll":    {window: true, element: true, geometry: "uia.scroll"},
		"uia.setvalue":  {window: true, element: true, text: true},
		"window.move":   {window: true, geometry: "move"},
		"window.resize": {window: true, geometry: "resize"},
		"scroll":        {window: true, geometry: "scroll"},
	}
	rule, ok := rules[action]
	return rule, ok
}

func validateRequest(req Request, now time.Time) error {
	if req.Validate() != nil || !validEnvelope(req, now) {
		return ErrInvalidRequest
	}
	rule, ok := requestRule(req.Action)
	if !ok {
		return ErrInvalidRequest
	}
	if !validActionFields(req, rule) {
		return ErrInvalidRequest
	}
	if rule.launch {
		return validateLaunch(req)
	}
	if req.Executable != "" || len(req.Arguments) != 0 {
		return ErrInvalidRequest
	}
	return nil
}

func validEnvelope(req Request, now time.Time) bool {
	deadline, err := time.Parse(time.RFC3339Nano, req.Deadline)
	if err != nil || !deadline.After(now) || deadline.After(now.Add(5*time.Minute)) {
		return false
	}
	return lowerHexID(req.RequestID) && utf8.ValidString(req.Text) && len(req.Text) <= 16384 && len(utf16.Encode([]rune(req.Text))) <= 4096 && !strings.ContainsRune(req.Text, 0)
}

func validActionFields(req Request, rule actionRule) bool {
	if rule.window != (req.WindowID != "") || rule.element != (req.ElementID != "") {
		return false
	}
	if !validWindowBinding(req, rule.window) {
		return false
	}
	if rule.window && !windowIDValid(req.WindowID) {
		return false
	}
	if rule.element && !elementIDValid(req.ElementID) {
		return false
	}
	if !rule.text && req.Text != "" {
		return false
	}
	return validGeometry(req, rule.geometry)
}

func validGeometry(req Request, geometry string) bool {
	if !validPosition(req.X, req.Y) || !validAxis(req.Axis, geometry) {
		return false
	}
	switch geometry {
	case "move":
		return req.Width == 0 && req.Height == 0 && req.Delta == 0
	case "resize":
		return validResize(req) && req.X == 0 && req.Y == 0 && req.Delta == 0
	case "uia.scroll":
		return validUIAScroll(req)
	case "scroll":
		return validScroll(req) && req.Width == 0 && req.Height == 0
	default:
		return emptyGeometry(req)
	}
}

func validUIAScroll(req Request) bool {
	return req.X == 0 && req.Y == 0 && req.Width == 0 && req.Height == 0 && req.Delta != 0 && req.Delta >= -10 && req.Delta <= 10
}

func validAxis(axis, geometry string) bool {
	if geometry != "scroll" && geometry != "uia.scroll" {
		return axis == ""
	}
	return axis == "" || axis == "vertical" || axis == "horizontal"
}

func validPosition(x, y int) bool { return x >= -32768 && x <= 32768 && y >= -32768 && y <= 32768 }

func validResize(req Request) bool {
	return req.Width >= 1 && req.Height >= 1 && req.Width <= 16384 && req.Height <= 16384
}
func validScroll(req Request) bool { return req.Delta != 0 && req.Delta >= -1200 && req.Delta <= 1200 }

func emptyGeometry(req Request) bool {
	return req.X == 0 && req.Y == 0 && req.Width == 0 && req.Height == 0 && req.Delta == 0
}

func lowerHexID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, char := range id {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validWindowBinding(req Request, window bool) bool {
	if !window {
		return req.WindowIdentity == ""
	}
	if req.WindowIdentity == "" {
		return req.Action == "uia.tree"
	}
	parts := strings.Split(req.WindowIdentity, ":")
	if len(req.WindowIdentity) > 80 || len(parts) != 3 || parts[0] != req.WindowID {
		return false
	}
	for _, part := range parts[1:] {
		if !windowIDValid(part) {
			return false
		}
	}
	return true
}

func windowIDValid(id string) bool {
	if len(id) < 1 || len(id) > 19 || id[0] < '1' || id[0] > '9' {
		return false
	}
	for _, char := range id {
		if char < '0' || char > '9' {
			return false
		}
	}
	number, err := strconv.ParseInt(id, 10, 64)
	return err == nil && number > 0
}

func elementIDValid(id string) bool {
	if len(id) == 0 || len(id) > 256 {
		return false
	}
	for part := range strings.SplitSeq(id, ":") {
		if _, err := strconv.ParseInt(part, 10, 32); err != nil {
			return false
		}
	}
	return true
}

func validateLaunch(req Request) error {
	if !validExecutable(req.Executable) || len(req.Arguments) > 16 {
		return ErrInvalidRequest
	}
	total := 0
	for _, argument := range req.Arguments {
		if !utf8.ValidString(argument) || len(argument) > 1024 || strings.ContainsRune(argument, 0) {
			return ErrInvalidRequest
		}
		total += len(argument)
	}
	if total > 8192 {
		return ErrInvalidRequest
	}
	return nil
}

func validExecutable(path string) bool {
	if len(path) < 7 || len(path) > 1024 || !utf8.ValidString(path) {
		return false
	}
	if (path[0] < 'a' || path[0] > 'z') && (path[0] < 'A' || path[0] > 'Z') {
		return false
	}
	return path[1:3] == ":\\" && strings.HasSuffix(strings.ToLower(path), ".exe") && !strings.ContainsAny(path, "\x00\r\n\"")
}
