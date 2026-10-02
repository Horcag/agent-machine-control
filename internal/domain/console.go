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
	FrameID      string    `json:"frame_id"`
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
	Kind      string `json:"kind"`
	FrameID   string `json:"frame_id,omitempty"`
	X         int    `json:"x,omitempty"`
	Y         int    `json:"y,omitempty"`
	ToX       int    `json:"to_x,omitempty"`
	ToY       int    `json:"to_y,omitempty"`
	Button    string `json:"button,omitempty"`
	Key       string `json:"key,omitempty"`
	Text      string `json:"text,omitempty"`
	Delta     int    `json:"delta,omitempty"`
	Count     int    `json:"count,omitempty"`
	Modifiers string `json:"modifiers,omitempty"`
}
