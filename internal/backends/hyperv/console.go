package hyperv

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"time"

	"github.com/Horcag/agent-machine-control/internal/domain"
)

const ConsoleRequestEnvVar = "AMC_CONSOLE_REQUEST"

type consoleRequest struct {
	VMID   string               `json:"vm_id"`
	Width  int                  `json:"width,omitempty"`
	Height int                  `json:"height,omitempty"`
	Input  *domain.ConsoleInput `json:"input,omitempty"`
	Keys   []uint32             `json:"keys,omitempty"`
}

type consoleResponse struct {
	Success      bool   `json:"success"`
	VMID         string `json:"vm_id"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
	NativeWidth  int    `json:"native_width,omitempty"`
	NativeHeight int    `json:"native_height,omitempty"`
	RGB565       []byte `json:"rgb565,omitempty"`
}

func consoleEnvironment(request consoleRequest) []string {
	payload, _ := json.Marshal(request)
	return []string{ConsoleRequestEnvVar + "=" + base64.StdEncoding.EncodeToString(payload)}
}

// CaptureConsole captures only the exact guest display through Hyper-V WMI.
func (a *Adapter) CaptureConsole(ctx context.Context, id string, width, height int) (domain.ConsoleFrame, error) {
	id, err := domain.NormalizeMachineGUID(id)
	if err != nil {
		return domain.ConsoleFrame{}, err
	}
	if width == 0 && height == 0 {
		width, height = 1024, 768
	}
	if err = domain.ValidateConsoleDimensions(width, height); err != nil {
		return domain.ConsoleFrame{}, err
	}
	if err = a.rejectRemotePrivilegedRoute(); err != nil {
		return domain.ConsoleFrame{}, err
	}
	output, err := a.executeScript(ctx, ScriptConsoleCapture, consoleEnvironment(consoleRequest{VMID: id, Width: width, Height: height}))
	if err != nil {
		return domain.ConsoleFrame{}, err
	}
	return a.parseConsoleFrame(output, id, width, height)
}

func (a *Adapter) parseConsoleFrame(output []byte, id string, width, height int) (domain.ConsoleFrame, error) {
	var result consoleResponse
	if err := decodeStrictJSON(output, &result); err != nil {
		return domain.ConsoleFrame{}, ErrMalformedResponse
	}
	if !result.Success {
		return domain.ConsoleFrame{}, ErrHostUnavailable
	}
	if result.VMID != id || result.Width != width || result.Height != height || result.NativeWidth < 1 || result.NativeHeight < 1 || result.NativeWidth > 65535 || result.NativeHeight > 65535 {
		return domain.ConsoleFrame{}, ErrMalformedResponse
	}
	data, err := encodeConsoleRGB565(result.RGB565, width, height)
	if err != nil {
		return domain.ConsoleFrame{}, err
	}
	sum := sha256.Sum256(data)
	return domain.ConsoleFrame{VMID: id, Width: width, Height: height, NativeWidth: result.NativeWidth, NativeHeight: result.NativeHeight, MIMEType: "image/png", SHA256: hex.EncodeToString(sum[:]), ObservedAt: a.now(), Data: data}, nil
}

func encodeConsoleRGB565(raw []byte, width, height int) ([]byte, error) {
	if domain.ValidateConsoleDimensions(width, height) != nil {
		return nil, ErrMalformedResponse
	}
	// Observed native capture buffers also include four trailing bytes.
	// Only the requested RGB565 pixels enter the PNG. Other lengths fail closed.
	pixelBytes := width * height * 2
	if len(raw) != pixelBytes && len(raw) != pixelBytes+4 {
		return nil, ErrMalformedResponse
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for i := 0; i < width*height; i++ {
		pixel := binary.LittleEndian.Uint16(raw[i*2:])
		r, g, b := uint8(pixel>>11), uint8((pixel>>5)&63), uint8(pixel&31)
		img.SetRGBA(i%width, i/width, color.RGBA{R: (r << 3) | (r >> 2), G: (g << 2) | (g >> 4), B: (b << 3) | (b >> 2), A: 255})
	}
	var output bytes.Buffer
	if err := png.Encode(&output, img); err != nil {
		return nil, ErrMalformedResponse
	}
	return output.Bytes(), nil
}

// SendConsoleInput targets guest WMI devices, never the host input desktop.
func (a *Adapter) SendConsoleInput(ctx context.Context, id string, input domain.ConsoleInput) error {
	id, err := domain.NormalizeMachineGUID(id)
	if err != nil {
		return err
	}
	if err = input.Validate(); err != nil {
		return err
	}
	if err = a.rejectRemotePrivilegedRoute(); err != nil {
		return err
	}
	request := consoleRequest{VMID: id, Input: &input}
	if input.Kind == "key" {
		request.Keys, _ = domain.ConsoleKeyCodes(input.Key)
	}
	output, err := a.executeScript(ctx, ScriptConsoleInput, consoleEnvironment(request))
	if err == nil {
		var result consoleResponse
		switch {
		case decodeStrictJSON(output, &result) != nil, result.VMID != id:
			err = ErrMalformedResponse
		case !result.Success:
			err = ErrHostUnavailable
		}
	}
	if err != nil {
		a.releaseConsoleInput(ctx, request)
	}
	return err
}

func (a *Adapter) releaseConsoleInput(ctx context.Context, request consoleRequest) {
	kind := request.Input.Kind
	if kind != "key" && kind != "click" && kind != "drag" {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_, _ = a.executeScript(cleanupCtx, ScriptConsoleCleanup, consoleEnvironment(request))
}
