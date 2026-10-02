package domain

// DesktopRequest describes one guest desktop action in native screen coordinates.
// Requests cross an authenticated guest transport; they never address the host UI.
type DesktopRequest struct {
	RequestID      string   `json:"request_id,omitempty" jsonschema:"Optional in MCP: generated from the idempotency key; otherwise 32 lowercase hexadecimal characters"`
	Deadline       string   `json:"deadline,omitempty" jsonschema:"Optional in MCP: copied from the outer deadline; otherwise an RFC3339 future time"`
	Action         string   `json:"action" jsonschema:"One of status cursor windows uia.tree clipboard.get provision remove window.focus window.move window.resize window.minimize window.maximize window.restore window.close uia.invoke uia.setvalue uia.select uia.toggle uia.expand uia.collapse uia.scroll clipboard.set wheel launch"`
	WindowID       string   `json:"window_id,omitempty" jsonschema:"Decimal HWND copied from desktop_observe; pair with its window identity"`
	WindowIdentity string   `json:"window_identity,omitempty" jsonschema:"Copy the observed window identity exactly to reject reused HWNDs and changed processes"`
	ElementID      string   `json:"element_id,omitempty" jsonschema:"Copy the element ID from a fresh UI Automation tree; choose an action advertised in its patterns"`
	X              int      `json:"x,omitempty" jsonschema:"Native guest screen pixel X; use native_width rather than scaled PNG width"`
	Y              int      `json:"y,omitempty" jsonschema:"Native guest screen pixel Y; use native_height rather than scaled PNG height"`
	Width          int      `json:"width,omitempty"`
	Height         int      `json:"height,omitempty"`
	Delta          int      `json:"delta,omitempty" jsonschema:"Wheel delta or UI Automation scroll amount; positive and negative directions are supported"`
	Axis           string   `json:"axis,omitempty" jsonschema:"vertical or horizontal; defaults to vertical"`
	Text           string   `json:"text,omitempty"`
	Executable     string   `json:"executable,omitempty" jsonschema:"Absolute Windows executable path for launch"`
	Arguments      []string `json:"arguments,omitempty"`
}

type DesktopBounds struct {
	Left   int `json:"left"`
	Top    int `json:"top"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type DesktopCursor struct {
	X       int  `json:"x"`
	Y       int  `json:"y"`
	Visible bool `json:"visible"`
}

type DesktopWindow struct {
	ID        string        `json:"id"`
	Identity  string        `json:"identity"`
	Title     string        `json:"title"`
	ClassName string        `json:"class_name"`
	ProcessID int           `json:"process_id"`
	Bounds    DesktopBounds `json:"bounds"`
	State     string        `json:"state"`
}

type DesktopElement struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	AutomationID string        `json:"automation_id"`
	ControlType  string        `json:"control_type"`
	Bounds       DesktopBounds `json:"bounds"`
	Enabled      bool          `json:"enabled"`
	Offscreen    bool          `json:"offscreen"`
	Patterns     []string      `json:"patterns"`
}

type DesktopResponse struct {
	RequestID string           `json:"request_id"`
	Success   bool             `json:"success"`
	Error     string           `json:"error,omitempty"`
	SessionID int              `json:"session_id"`
	Elevated  bool             `json:"elevated"`
	Cursor    *DesktopCursor   `json:"cursor,omitempty"`
	Windows   []DesktopWindow  `json:"windows,omitempty"`
	Elements  []DesktopElement `json:"elements,omitempty"`
	Text      string           `json:"text,omitempty"`
	ProcessID int              `json:"process_id,omitempty"`
}
