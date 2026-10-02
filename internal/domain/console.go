package domain

import "time"

const (
	CapabilityConsoleScreenshot = "console.screenshot"
	CapabilityConsoleInput      = "console.input"
	MaxConsolePixels            = 1024 * 1024
)

// ConsoleFrame contains an image of one VM console, never the host desktop.
type ConsoleFrame struct {
	VMID         string    `json:"vm_id"`
	FrameID      string    `json:"frame_id" jsonschema:"Fresh frame_id from console_screenshot or desktop_observe; required for pointer actions"`
	Width        int       `json:"width"`
	Height       int       `json:"height"`
	NativeWidth  int       `json:"native_width"`
	NativeHeight int       `json:"native_height"`
	MIMEType     string    `json:"mime_type"`
	SHA256       string    `json:"sha256"`
	ObservedAt   time.Time `json:"observed_at"`
	Data         []byte    `json:"data,omitempty"`
}

// ConsoleInput describes one bounded action in a VM's console coordinates.
// Pointer actions use a frame ID and coordinates in that captured image.
type ConsoleInput struct {
	Kind      string `json:"kind" jsonschema:"One of move click drag key type; wheel uses a guest desktop action"`
	FrameID   string `json:"frame_id,omitempty"`
	X         int    `json:"x,omitempty" jsonschema:"X in the captured PNG pixels; the backend scales to the guest native screen"`
	Y         int    `json:"y,omitempty" jsonschema:"Y in the captured PNG pixels; the backend scales to the guest native screen"`
	ToX       int    `json:"to_x,omitempty" jsonschema:"Drag endpoint X in the captured PNG pixels"`
	ToY       int    `json:"to_y,omitempty" jsonschema:"Drag endpoint Y in the captured PNG pixels"`
	Button    string `json:"button,omitempty" jsonschema:"left right or middle; defaults to left"`
	Key       string `json:"key,omitempty" jsonschema:"Key or chord such as enter ctrl+s alt+f4 or ctrl+shift+esc"`
	Text      string `json:"text,omitempty" jsonschema:"Unicode text typed into the guest active window"`
	Delta     int    `json:"delta,omitempty"`
	Count     int    `json:"count,omitempty" jsonschema:"Click count: 1 or 2; defaults to 1"`
	Modifiers string `json:"modifiers,omitempty" jsonschema:"Pointer modifiers joined by + such as ctrl+shift; supports ctrl alt shift win"`
}
