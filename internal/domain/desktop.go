package domain

// DesktopRequest describes one guest desktop action in native screen coordinates.
// Requests cross an authenticated guest transport; they never address the host UI.
type DesktopRequest struct {
	RequestID  string   `json:"request_id"`
	Deadline   string   `json:"deadline"`
	Action     string   `json:"action"`
	WindowID   string   `json:"window_id,omitempty"`
	ElementID  string   `json:"element_id,omitempty"`
	X          int      `json:"x,omitempty"`
	Y          int      `json:"y,omitempty"`
	Width      int      `json:"width,omitempty"`
	Height     int      `json:"height,omitempty"`
	Delta      int      `json:"delta,omitempty"`
	Text       string   `json:"text,omitempty"`
	Executable string   `json:"executable,omitempty"`
	Arguments  []string `json:"arguments,omitempty"`
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
