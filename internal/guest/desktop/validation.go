package desktop

import (
	"encoding/json"
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
		"status": {}, "cursor": {}, "windows": {}, "clipboard.get": {}, "clipboard.snapshot": {},
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

type clipboardWireMetadata struct {
	Sequence *uint32   `json:"sequence"`
	Formats  *[]uint32 `json:"formats"`
	Complete *bool     `json:"inventory_complete"`
	Empty    *bool     `json:"empty"`
}

// Old helpers may return only text for a plain get; guarded writes require the
// complete metadata protocol and exact readback from the helper's clipboard lock.
func validClipboardResponse(data []byte, response Response, req Request) bool {
	switch req.Action {
	case "clipboard.snapshot":
		return validClipboardSnapshotResponse(data)
	case "clipboard.get", "clipboard.set.guarded":
		return validClipboardReadback(data, response, req)
	default:
		return true
	}
}

// Text reads and guarded writes retain the legacy readback compatibility contract.
func validClipboardReadback(data []byte, response Response, req Request) bool {
	guarded := req.Action == "clipboard.set.guarded"
	if !utf8.Valid(data) || !validClipboardText(response.Text) {
		return false
	}
	var wire struct {
		Text      json.RawMessage        `json:"text"`
		Clipboard *clipboardWireMetadata `json:"clipboard"`
	}
	if json.Unmarshal(data, &wire) != nil || !validJSONUnicode(wire.Text) {
		return false
	}
	if (guarded || wire.Clipboard != nil) && len(wire.Text) == 0 {
		return false
	}
	if guarded && response.Text != req.Text {
		return false
	}
	if wire.Clipboard == nil {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(data, &fields)
		_, present := fields["clipboard"]
		return !guarded && !present
	}
	return validClipboardInventory(wire.Clipboard, response.Text, guarded && req.Text == "")
}

// Inventory observations never accept payload-bearing response fields, even empty ones.
// Older helpers must supply this complete protocol; there is no text-get fallback.
func validClipboardSnapshotResponse(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return false
	}
	for field := range fields {
		switch field {
		case "request_id", "success", "session_id", "elevated", "clipboard":
		default:
			return false
		}
	}
	var meta clipboardWireMetadata
	return json.Unmarshal(fields["clipboard"], &meta) == nil && validClipboardInventory(&meta, "", false)
}

func validClipboardInventory(meta *clipboardWireMetadata, text string, clearing bool) bool {
	if meta.Sequence == nil || meta.Formats == nil || meta.Complete == nil || !*meta.Complete || meta.Empty == nil {
		return false
	}
	formats := *meta.Formats
	if len(formats) > 256 || *meta.Empty != (len(formats) == 0) {
		return false
	}
	if clearing {
		return len(formats) == 0
	}
	hasUnicodeText := false
	for i, format := range formats {
		if format == 0 || (i > 0 && format <= formats[i-1]) {
			return false
		}
		hasUnicodeText = hasUnicodeText || format == 13
	}
	return text == "" || hasUnicodeText
}

func validClipboardText(text string) bool {
	return utf8.ValidString(text) && !strings.ContainsRune(text, 0) && len(utf16.Encode([]rune(text))) <= 4096
}

// encoding/json replaces lone escaped surrogates with U+FFFD. Check the wire
// string first so malformed UTF-16 cannot be accepted as a successful readback.
func validJSONUnicode(raw json.RawMessage) bool {
	if len(raw) != 0 && raw[0] != '"' {
		return false
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) || raw[i] != 'u' {
			continue
		}
		// The caller has already decoded this JSON, proving the escape is six bytes.
		code, _ := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		i += 4
		if code >= 0xdc00 && code <= 0xdfff {
			return false
		}
		if code < 0xd800 || code > 0xdbff {
			continue
		}
		if i+6 >= len(raw) || string(raw[i+1:i+3]) != "\\u" {
			return false
		}
		low, _ := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
		if low < 0xdc00 || low > 0xdfff {
			return false
		}
		i += 6
	}
	return true
}
